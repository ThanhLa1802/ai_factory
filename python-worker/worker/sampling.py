"""Sampling loop tự viết — greedy / temperature / top-k / top-p (nucleus).

Tuần 5-6: thay các tham số `model.generate()` (`do_sample`/`temperature`/`top_p`/
`top_k`) bằng vòng lặp autoregressive tự viết trong file này. Vòng lặp **vẫn
dùng forward pass + KV cache của HF** (`past_key_values`) — Tuần 7-8 sẽ tự quản
lý KV cache và bỏ hẳn `model.generate()`.

Phần toán sampling là hàm thuần trên tensor, tách khỏi model để test được trên CPU.
"""

from dataclasses import dataclass
from typing import Callable, Iterable, Optional

import torch
import torch.nn.functional as F

# ---------------------------------------------------------------------------
# Tham số sampling
# ---------------------------------------------------------------------------

@dataclass
class SamplingParams:
    """Cùng ngữ nghĩa với `SamplingParams` trong proto/inference.proto."""

    max_tokens: int = 1024
    temperature: float = 0.7
    top_k: int = 50
    top_p: float = 0.9

    @classmethod
    def from_dict(cls, d: Optional[dict]) -> "SamplingParams":
        d = d or {}
        return cls(
            max_tokens=d.get("max_tokens", 1024),
            temperature=d.get("temperature", 0.7),
            top_k=d.get("top_k", 50),
            top_p=d.get("top_p", 0.9),
        )


# ---------------------------------------------------------------------------
# Các bước biến đổi logits (thuần, không phụ thuộc model)
# ---------------------------------------------------------------------------

def apply_temperature(logits: torch.Tensor, temperature: float) -> torch.Tensor:
    """Chia logits cho temperature. `temperature <= 0` để nguyên (greedy lo phần đó)."""
    if temperature <= 0:
        return logits
    return logits / temperature


def apply_top_k(logits: torch.Tensor, top_k: int) -> torch.Tensor:
    """Giữ `top_k` logits cao nhất, còn lại `-inf`.

    `top_k <= 0` hoặc `top_k >= vocab` → coi như tắt (trả nguyên trạng).
    """
    if top_k <= 0:
        return logits
    vocab = logits.shape[-1]
    if top_k >= vocab:
        return logits
    kth_value = torch.topk(logits, top_k, dim=-1).values[..., -1, None]
    return torch.where(logits < kth_value, torch.full_like(logits, float("-inf")), logits)


def apply_top_p(logits: torch.Tensor, top_p: float) -> torch.Tensor:
    """Nucleus (top-p) sampling: giữ tập nhỏ nhất có tổng xác suất >= `top_p`.

    Cùng thuật toán HF `TopPLogitsWarper` — luôn giữ ít nhất 1 token (token cao nhất).
    """
    if top_p is None or top_p >= 1.0:
        return logits
    if top_p < 0:
        top_p = 0.0

    sorted_logits, sorted_idx = torch.sort(logits, descending=True, dim=-1)
    probs = F.softmax(sorted_logits, dim=-1)
    cumulative = torch.cumsum(probs, dim=-1)

    # Bỏ các token vượt ngưỡng; dịch phải 1 ô để giữ token chạm ngưỡng.
    remove = cumulative > top_p
    remove = remove.roll(1, dims=-1)
    remove[..., 0] = False
    sorted_logits = sorted_logits.masked_fill(remove, float("-inf"))

    out = torch.full_like(logits, float("-inf"))
    out.scatter_(-1, sorted_idx, sorted_logits)
    return out


def sample_next(
    logits: torch.Tensor,
    params: SamplingParams,
    generator: Optional[torch.Generator] = None,
) -> torch.Tensor:
    """Chọn token kế tiếp cho batch `logits` shape `[B, V]` → trả id shape `[B]`.

    - `temperature <= 0` → greedy (argmax), bỏ qua top-k/top-p.
    - Ngược lại: temperature → top-k → top-p → multinomial.
    """
    if params.temperature <= 0:
        return torch.argmax(logits, dim=-1)

    scaled = apply_temperature(logits, params.temperature)
    scaled = apply_top_k(scaled, params.top_k)
    scaled = apply_top_p(scaled, params.top_p)
    probs = F.softmax(scaled, dim=-1)
    return torch.multinomial(probs, num_samples=1, generator=generator).squeeze(-1)


def sample_next_batch(
    logits: torch.Tensor,
    params_list: list[SamplingParams],
    generator: Optional[torch.Generator] = None,
) -> torch.Tensor:
    """Sample cho từng hàng với tham số riêng (batch nhỏ ≤ 4 nên loop ổn)."""
    batch = logits.shape[0]
    if len(params_list) == 1 and batch > 1:
        params_list = params_list * batch
    ids = torch.empty(batch, dtype=torch.long, device=logits.device)
    for i in range(batch):
        ids[i] = sample_next(logits[i : i + 1], params_list[i], generator)
    return ids


# ---------------------------------------------------------------------------
# Vòng lặp autoregressive
# ---------------------------------------------------------------------------

def generate_tokens(
    model,
    input_ids: torch.Tensor,
    attention_mask: torch.Tensor,
    params_list: list[SamplingParams],
    stop_ids: Iterable[int],
    generator: Optional[torch.Generator] = None,
    should_stop: Optional[Callable[[], bool]] = None,
):
    """Vòng lặp sinh token tự viết (blocking) — dùng forward pass + KV cache của HF.

    Yield `(row, token_id, reason)` sau mỗi bước decode:

    - `reason is None`   : token bình thường, caller phát text ra client
    - `reason == "stop"` : token là stop id (eos/pad) — KHÔNG phát ra client
    - `reason == "length"`: token chạm `max_tokens` của hàng đó

    Dừng hàng khi gặp stop id hoặc đạt `max_tokens`; dừng cả vòng lặp khi mọi hàng
    xong (hoặc `should_stop()` trả True — dùng cho cancel). Caller chạy trong thread
    riêng vì forward pass là blocking.
    """
    batch = input_ids.shape[0]
    device = input_ids.device
    stop_ids = set(stop_ids)

    past = None
    cur_ids = input_ids
    attn = attention_mask

    num_new = [0] * batch
    done = [p.max_tokens <= 0 for p in params_list]

    while not all(done):
        if should_stop is not None and should_stop():
            return

        out = model(
            input_ids=cur_ids,
            attention_mask=attn,
            past_key_values=past,
            use_cache=True,
        )
        past = out.past_key_values
        logits = out.logits[:, -1, :]
        next_ids = sample_next_batch(logits, params_list, generator)

        for i in range(batch):
            if done[i]:
                continue
            tid = int(next_ids[i].item())
            num_new[i] += 1
            if tid in stop_ids:
                done[i] = True
                yield i, tid, "stop"
            elif num_new[i] >= params_list[i].max_tokens:
                done[i] = True
                yield i, tid, "length"
            else:
                yield i, tid, None

        # Nối token mới vào sequence + attention mask để làm input bước sau.
        cur_ids = next_ids.unsqueeze(-1)
        attn = torch.cat(
            [attn, torch.ones((batch, 1), dtype=attn.dtype, device=device)], dim=1
        )
