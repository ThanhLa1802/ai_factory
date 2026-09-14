"""Test doubles cho continuous batching (CPU-only, không cần model thật).

- `FakeHFModel`: mô phỏng interface HF (`__call__ -> .logits + .past_key_values`)
  với cache là legacy-tuple `[(k, v)]` shape `[B, 1, S, 1]`, lưu chính token id.
  Logits của vị trí cuối suy từ **tổng toàn bộ history hợp lệ** → assembly cache
  sai (thiếu/thừa token, mask sai) sẽ đổi output.
- `FakeTokenizer`: `.encode(text)`, `eos_token_id`/`pad_token_id`.
- `FakeDecoder`: `put(tid) -> str` (chr), dùng thay `StreamingDecoder`.
"""

import torch


class FakeOutput:
    def __init__(self, logits, past_key_values):
        self.logits = logits
        self.past_key_values = past_key_values


class FakeHFModel:
    def __init__(self, vocab: int = 64, num_layers: int = 2):
        self.vocab = vocab
        self.num_layers = num_layers

    def __call__(
        self,
        input_ids,
        attention_mask=None,
        position_ids=None,
        past_key_values=None,
        use_cache: bool = True,
        **kwargs,
    ):
        B, q = input_ids.shape
        logits = torch.full((B, q, self.vocab), -20.0)

        for b in range(B):
            if past_key_values is not None:
                pk = past_key_values[0][0]  # [B, 1, L, 1]
                L = pk.shape[2]
                if attention_mask is not None:
                    mask = attention_mask[b, :L].bool()
                else:
                    mask = torch.ones(L, dtype=torch.bool)
                hist = pk[b, 0, :, 0][mask].long().tolist()
            else:
                hist = []
            cur = input_ids[b].tolist()
            for pos in range(q):
                nxt = (sum(hist + cur[: pos + 1]) + 1) % self.vocab
                logits[b, pos, nxt] = 20.0

        new_ids = input_ids.float().view(B, 1, q, 1)
        if past_key_values is None:
            past = tuple((new_ids.clone(), new_ids.clone()) for _ in range(self.num_layers))
        else:
            past = tuple(
                (torch.cat([k, new_ids], dim=2), torch.cat([v, new_ids], dim=2))
                for k, v in past_key_values
            )
        return FakeOutput(logits, past)


class FakeTokenizer:
    """Mã hoá text thành id theo ký tự (base 1 để 0 dành cho pad)."""

    eos_token_id = 999
    pad_token_id = 998

    def encode(self, text: str) -> list:
        return [ord(ch) % 5000 + 1 for ch in text]


class FakeDecoder:
    """`put(tid) -> chr` bỏ qua special/pad; đủ để test luồng event."""

    def put(self, tid: int) -> str:
        if tid in (FakeTokenizer.eos_token_id, FakeTokenizer.pad_token_id):
            return ""
        return chr(tid)
    def flush(self) -> str:
        return ""


def decoder_factory():
    return FakeDecoder()
