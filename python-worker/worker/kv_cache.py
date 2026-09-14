"""KV cache tự quản cho continuous batching engine.

Vòng đời: prefill (HF trả `past`) → buffer per-sequence do ta sở hữu → decode
(append K/V mỗi bước) → evict (`free()`). `KVCacheManager` assemble batch nhiều
độ dài bằng **left-pad + position_ids tường minh**; HF chỉ chạy attention cho
một bước (Tuần 9+ mới thay forward pass).

Layout cache giữ theo dạng legacy-tuple của HF: `list[(k, v)]` mỗi layer, với
k/v shape `[1, H, capacity, D]` cho một sequence.
"""

from dataclasses import dataclass
from typing import Optional

import torch


class KVCache:
    """KV cache của MỘT sequence, buffer tự cấp phát + tăng theo bội số."""

    def __init__(self):
        self.layers: list[tuple[torch.Tensor, torch.Tensor]] = []
        self.length = 0
        self.capacity = 0

    def init_from_prefill(self, past, prompt_len: int, row: int = 0) -> None:
        """Sở hữu cache HF trả về prefill cho `row`.

        Với prefill batch left-pad, token hợp lệ của row nằm ở **cuối** trục S,
        nên cắt `-prompt_len:` (đúng cho cả trường hợp không pad).
        """
        self.layers = []
        cap = max(prompt_len, 1)
        for k, v in past:
            kb = k.new_zeros((1, k.shape[1], cap, k.shape[3]))
            vb = v.new_zeros((1, v.shape[1], cap, v.shape[3]))
            kb[:, :, :prompt_len, :] = k[row : row + 1, :, -prompt_len:, :]
            vb[:, :, :prompt_len, :] = v[row : row + 1, :, -prompt_len:, :]
            self.layers.append((kb, vb))
        self.length = prompt_len
        self.capacity = cap

    def _ensure_capacity(self, needed: int) -> None:
        if needed <= self.capacity:
            return
        new_cap = max(self.capacity, 1)
        while new_cap < needed:
            new_cap *= 2
        grown = []
        for k, v in self.layers:
            nk = k.new_zeros((k.shape[0], k.shape[1], new_cap, k.shape[3]))
            nv = v.new_zeros((v.shape[0], v.shape[1], new_cap, v.shape[3]))
            nk[:, :, : self.length, :] = k[:, :, : self.length, :]
            nv[:, :, : self.length, :] = v[:, :, : self.length, :]
            grown.append((nk, nv))
        self.layers = grown
        self.capacity = new_cap

    def append_from_output(self, past, row: int) -> None:
        """Lấy slot cuối của `past[row]` (token vừa sinh) ghi vào buffer."""
        self._ensure_capacity(self.length + 1)
        for i, (k, v) in enumerate(self.layers):
            k[:, :, self.length : self.length + 1, :] = past[i][0][row : row + 1, :, -1:, :]
            v[:, :, self.length : self.length + 1, :] = past[i][1][row : row + 1, :, -1:, :]
        self.length += 1

    def view(self) -> list[tuple[torch.Tensor, torch.Tensor]]:
        """Trả cache đã cắt theo `length` (dùng để assemble batch)."""
        return [(k[:, :, : self.length, :], v[:, :, : self.length, :]) for k, v in self.layers]

    def free(self) -> None:
        self.layers = []
        self.length = 0
        self.capacity = 0


@dataclass
class StepInputs:
    """Input cho một forward pass (prefill hoặc decode)."""

    input_ids: torch.Tensor
    attention_mask: torch.Tensor
    position_ids: torch.Tensor
    past_key_values: Optional[tuple]


class KVCacheManager:
    """Assemble batch nhiều độ dài từ các `KVCache` rời."""

    @staticmethod
    def _build(attention: torch.Tensor) -> torch.Tensor:
        # Vị trí thật = cumsum(mask) - 1; vùng pad (mask 0) bị mask nên giá trị 0 vô hại.
        return (attention.cumsum(dim=1) - 1).clamp(min=0)

    def build_prefill(self, prompt_ids: list[torch.Tensor]) -> StepInputs:
        batch = len(prompt_ids)
        max_len = max(int(p.shape[0]) for p in prompt_ids)
        device = prompt_ids[0].device
        input_ids = torch.zeros((batch, max_len), dtype=torch.long, device=device)
        attention = torch.zeros((batch, max_len), dtype=torch.long, device=device)
        for i, p in enumerate(prompt_ids):
            n = int(p.shape[0])
            input_ids[i, max_len - n :] = p
            attention[i, max_len - n :] = 1
        return StepInputs(input_ids, attention, self._build(attention), None)

    def build_decode(
        self, caches: list[KVCache], pending_inputs: list[torch.Tensor]
    ) -> StepInputs:
        batch = len(caches)
        device = pending_inputs[0].device
        lengths = [c.length for c in caches]
        max_len = max(lengths)

        input_ids = torch.stack([t.reshape(1) for t in pending_inputs], dim=0)
        attention = torch.zeros((batch, max_len + 1), dtype=torch.long, device=device)
        for i, n in enumerate(lengths):
            attention[i, max_len - n :] = 1
        position_ids = torch.tensor(lengths, dtype=torch.long, device=device).unsqueeze(-1)

        num_layers = len(caches[0].layers)
        past = []
        for layer in range(num_layers):
            parts_k, parts_v = [], []
            for i, cache in enumerate(caches):
                k, v = cache.layers[layer]
                k = k[:, :, : lengths[i], :]
                v = v[:, :, : lengths[i], :]
                pad = max_len - lengths[i]
                if pad > 0:
                    k = torch.cat(
                        [k.new_zeros((k.shape[0], k.shape[1], pad, k.shape[3])), k], dim=2
                    )
                    v = torch.cat(
                        [v.new_zeros((v.shape[0], v.shape[1], pad, v.shape[3])), v], dim=2
                    )
                parts_k.append(k)
                parts_v.append(v)
            past.append((torch.cat(parts_k, dim=0), torch.cat(parts_v, dim=0)))

        return StepInputs(input_ids, attention, position_ids, tuple(past))

    @staticmethod
    def total_tokens(caches: list[KVCache]) -> int:
        return sum(c.length for c in caches)
