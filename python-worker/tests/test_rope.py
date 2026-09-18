"""Parity tests cho RoPE tự viết (CPU-only)."""

import torch
from transformers.models.qwen2.modeling_qwen2 import apply_rotary_pos_emb

from tiny_qwen import build_tiny_qwen
from worker.model.rope import RotaryEmbedding, rotate_half


def _rope_from_model(model):
    head_dim = model.config.hidden_size // model.config.num_attention_heads
    return RotaryEmbedding(head_dim=head_dim, theta=float(model.config.rope_theta))


def test_cos_sin_matches_hf():
    model = build_tiny_qwen()
    rope = _rope_from_model(model)
    S = 6
    pos = torch.arange(S).unsqueeze(0)
    cos, sin = rope.cos_sin(pos, dtype=torch.float32)

    hf_cos, hf_sin = model.model.rotary_emb(torch.zeros(1, S, model.config.hidden_size), pos)

    assert cos.shape == hf_cos.shape
    assert torch.allclose(cos, hf_cos, atol=1e-5)
    assert torch.allclose(sin, hf_sin, atol=1e-5)


def test_cos_sin_offset_positions_match_hf():
    """position_ids có offset (decode sau past) — chỗ dễ sai nhất."""
    model = build_tiny_qwen()
    rope = _rope_from_model(model)
    pos = torch.tensor([[7, 8, 9]])
    cos, sin = rope.cos_sin(pos, dtype=torch.float32)
    hf_cos, hf_sin = model.model.rotary_emb(torch.zeros(1, 3, model.config.hidden_size), pos)
    assert torch.allclose(cos, hf_cos, atol=1e-5)
    assert torch.allclose(sin, hf_sin, atol=1e-5)


def test_rotate_half_semantics():
    x = torch.tensor([[1.0, 2.0, 3.0, 4.0]])
    assert torch.equal(rotate_half(x), torch.tensor([[-3.0, -4.0, 1.0, 2.0]]))


def test_apply_matches_hf():
    model = build_tiny_qwen()
    rope = _rope_from_model(model)
    B, Hq, Hkv, S, D = 2, 8, 2, 5, 8
    q = torch.randn(B, Hq, S, D)
    k = torch.randn(B, Hkv, S, D)
    pos = torch.arange(S).unsqueeze(0).expand(B, -1)

    cos, sin = rope.cos_sin(pos, dtype=q.dtype)
    q1, k1 = rope.apply(q, k, cos, sin)
    q2, k2 = apply_rotary_pos_emb(q, k, cos, sin)

    assert q1.shape == q.shape
    assert k1.shape == k.shape
    assert torch.allclose(q1, q2, atol=1e-5)
    assert torch.allclose(k1, k2, atol=1e-5)
