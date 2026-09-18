"""Forward pass tự viết cho Qwen2 — layer loop + attention GQA + RoPE.

Thay `Qwen2Model.forward` của HF: ta tự chạy QKV projection, RoPE, attention,
causal/padding mask, MLP và nối KV cache. **Tái dùng leaf module** của HF
(`embed_tokens`, `layers[i].self_attn.{q,k,v,o}_proj`, `layers[i].mlp`, các
RMSNorm, `model.norm`, `lm_head`) vì weights là 4-bit NF4 (không dequant — D1).

Interface tương thích HF để `ContinuousBatchEngine` không phải sửa (D2):

    forward(input_ids, attention_mask, position_ids, past_key_values, use_cache)
      -> object(.logits, .past_key_values)

`past_key_values` vào/ra ở dạng **legacy tuple** `(k, v)` mỗi layer, shape
`[B, H_kv, S, D]` (GQA không expand — D3). Đầu vào cũng chấp nhận `Cache` của HF
(tự chuyển qua `to_legacy_cache()`).
"""

from typing import Optional

import torch

from .attention import gqa_attention
from .rope import RotaryEmbedding


class ForwardOutput:
    def __init__(self, logits: torch.Tensor, past_key_values):
        self.logits = logits
        self.past_key_values = past_key_values


def _as_legacy(past_key_values):
    if past_key_values is None:
        return None
    if hasattr(past_key_values, "to_legacy_cache"):
        return past_key_values.to_legacy_cache()
    return past_key_values


class Qwen2Forward:
    """Forward pass Qwen2 tự viết, bọc một `Qwen2ForCausalLM` của HF."""

    def __init__(self, hf_model):
        cfg = hf_model.config
        self.model = hf_model.model
        self.lm_head = hf_model.lm_head
        self.layers = hf_model.model.layers
        self.num_heads = cfg.num_attention_heads
        self.num_kv_heads = cfg.num_key_value_heads
        self.head_dim = cfg.hidden_size // cfg.num_attention_heads
        self.rope = RotaryEmbedding(
            self.head_dim, theta=float(getattr(cfg, "rope_theta", 1e6))
        )

    def __call__(
        self,
        input_ids: torch.Tensor,
        attention_mask: Optional[torch.Tensor] = None,
        position_ids: Optional[torch.Tensor] = None,
        past_key_values=None,
        use_cache: bool = True,
        **kwargs,
    ) -> ForwardOutput:
        B, Sq = input_ids.shape
        past = _as_legacy(past_key_values)
        past_len = int(past[0][0].shape[2]) if past is not None else 0

        h = self.model.embed_tokens(input_ids)

        if position_ids is None:
            position_ids = torch.arange(
                past_len, past_len + Sq, device=input_ids.device
            ).unsqueeze(0)

        cos, sin = self.rope.cos_sin(position_ids, dtype=h.dtype)
        causal = Sq > 1

        new_kv = []
        for idx, layer in enumerate(self.layers):
            attn = layer.self_attn
            residual = h
            x = layer.input_layernorm(h)

            q = attn.q_proj(x).view(B, Sq, self.num_heads, self.head_dim).transpose(1, 2)
            k = attn.k_proj(x).view(B, Sq, self.num_kv_heads, self.head_dim).transpose(1, 2)
            v = attn.v_proj(x).view(B, Sq, self.num_kv_heads, self.head_dim).transpose(1, 2)

            q, k = self.rope.apply(q, k, cos, sin)

            if past is not None:
                k_all = torch.cat([past[idx][0], k], dim=2)
                v_all = torch.cat([past[idx][1], v], dim=2)
            else:
                k_all, v_all = k, v

            out = gqa_attention(q, k_all, v_all, attention_mask=attention_mask, causal=causal)
            out = out.transpose(1, 2).contiguous().reshape(B, Sq, -1)
            h = residual + attn.o_proj(out)

            residual = h
            h = residual + layer.mlp(layer.post_attention_layernorm(h))

            new_kv.append((k_all, v_all))

        h = self.model.norm(h)
        logits = self.lm_head(h)

        return ForwardOutput(logits, tuple(new_kv) if use_cache else None)
