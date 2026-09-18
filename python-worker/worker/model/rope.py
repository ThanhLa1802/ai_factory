"""RoPE tự viết — GPT-NeoX style (`rotate_half`), dùng cho Qwen2.

Thay `Qwen2RotaryEmbedding` + `apply_rotary_pos_emb` của HF. Công thức khớp HF
(đã test parity trong `tests/test_rope.py`): `inv_freq = 1 / theta**(arange(0,D,2)/D)`,
`emb = cat(freqs, freqs)`, `q_embed = q*cos + rotate_half(q)*sin`.

`attention_scaling` của HF (mặc định 1.0 với rope_type "default") không cần vì
Qwen2.5-Coder không dùng rope_scaling.
"""

from typing import Optional

import torch


def rotate_half(x: torch.Tensor) -> torch.Tensor:
    """Đổi nửa sau lên trước và đảo dấu: [x1, x2] -> [-x2, x1]."""
    half = x.shape[-1] // 2
    x1 = x[..., :half]
    x2 = x[..., half:]
    return torch.cat((-x2, x1), dim=-1)


class RotaryEmbedding:
    def __init__(self, head_dim: int, theta: float = 1e6, device=None):
        self.head_dim = head_dim
        self.theta = float(theta)
        self.inv_freq = 1.0 / (
            self.theta
            ** (torch.arange(0, head_dim, 2, device=device).float() / head_dim)
        )

    def cos_sin(self, position_ids: torch.Tensor, dtype: Optional[torch.dtype] = None):
        """Trả `(cos, sin)` shape `[B, S, D]` cho `position_ids` `[B, S]`."""
        pos = position_ids.float()
        inv = self.inv_freq.to(pos.device)
        freqs = pos[:, :, None] * inv[None, None, :]
        emb = torch.cat((freqs, freqs), dim=-1)
        cos, sin = emb.cos(), emb.sin()
        if dtype is not None:
            cos, sin = cos.to(dtype), sin.to(dtype)
        return cos, sin

    def apply(self, q: torch.Tensor, k: torch.Tensor, cos: torch.Tensor, sin: torch.Tensor):
        """Áp RoPE lên q/k `[B, H, S, D]` với cos/sin `[B, S, D]`."""
        cos = cos.unsqueeze(1)
        sin = sin.unsqueeze(1)
        q_embed = (q * cos) + (rotate_half(q) * sin)
        k_embed = (k * cos) + (rotate_half(k) * sin)
        return q_embed, k_embed
