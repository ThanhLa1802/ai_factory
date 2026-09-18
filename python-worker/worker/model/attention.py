"""GQA attention tự viết — repeat_kv + mask (padding/causal) + softmax.

Tương đương `eager_attention_forward` của HF Qwen2 (test parity
`tests/test_self_attention.py`), nhưng tường minh để ta kiểm soát mask và
tương thích KV cache tự quản. Trả attention output `[B, Hq, Sq, D]`.
"""

import math
from typing import Optional

import torch


def repeat_kv(x: torch.Tensor, n_rep: int) -> torch.Tensor:
    """[B, Hkv, S, D] -> [B, Hkv*n_rep, S, D] (giống HF `repeat_kv`)."""
    B, Hkv, S, D = x.shape
    if n_rep == 1:
        return x
    return (
        x[:, :, None, :, :]
        .expand(B, Hkv, n_rep, S, D)
        .reshape(B, Hkv * n_rep, S, D)
    )


def _additive_mask(
    scores: torch.Tensor,
    attention_mask: Optional[torch.Tensor],
    causal: bool,
) -> torch.Tensor:
    B, _, Sq, Sk = scores.shape
    ok = None
    if attention_mask is not None:
        ok = attention_mask[:, None, None, :].bool()
    if causal:
        causal_ok = torch.ones(Sq, Sk, dtype=torch.bool, device=scores.device).tril(
            diagonal=Sk - Sq
        )[None, None, :, :]
        ok = causal_ok if ok is None else (ok & causal_ok)
    if ok is None:
        return scores
    min_dtype = torch.finfo(scores.dtype).min
    return scores.masked_fill(~ok, min_dtype)


def gqa_attention(
    q: torch.Tensor,
    k: torch.Tensor,
    v: torch.Tensor,
    attention_mask: Optional[torch.Tensor] = None,
    causal: bool = False,
) -> torch.Tensor:
    """Attention GQA.

    - q `[B, Hq, Sq, D]`; k/v `[B, Hkv, Sk, D]` (Hq là bội của Hkv).
    - `attention_mask` `[B, Sk]`: 1 = key hợp lệ, 0 = pad.
    - `causal=True`: chỉ cho query i attend key j <= i (dùng cho prefill).
    """
    n_rep = q.shape[1] // k.shape[1]
    k = repeat_kv(k, n_rep)
    v = repeat_kv(v, n_rep)

    scale = 1.0 / math.sqrt(q.shape[-1])
    scores = torch.matmul(q, k.transpose(-1, -2)) * scale
    scores = _additive_mask(scores, attention_mask, causal)

    probs = torch.softmax(scores, dim=-1, dtype=torch.float32).to(q.dtype)
    return torch.matmul(probs, v)
