"""Tests cho GQA attention tự viết (CPU-only)."""

import math

import torch

from worker.model.attention import gqa_attention, repeat_kv


def _ref_attention(q, k, v, key_ok=None):
    """Reference: MHA/GQA đơn giản, softmax fp32, không causal."""
    n_rep = q.shape[1] // k.shape[1]
    k = repeat_kv(k, n_rep)
    v = repeat_kv(v, n_rep)
    scores = torch.matmul(q, k.transpose(-1, -2)) / math.sqrt(q.shape[-1])
    if key_ok is not None:
        scores = scores.masked_fill(~key_ok[:, None, None, :], float("-inf"))
    probs = torch.softmax(scores, dim=-1, dtype=torch.float32).to(q.dtype)
    return torch.matmul(probs, v)


def test_repeat_kv_shape_and_values():
    x = torch.arange(2 * 2 * 3 * 1, dtype=torch.float32).reshape(2, 2, 3, 1)
    out = repeat_kv(x, 4)
    assert out.shape == (2, 8, 3, 1)
    for h in range(8):
        assert torch.equal(out[:, h], x[:, h // 4])


def test_output_shape_and_mha_reference():
    B, H, D, S = 2, 4, 8, 5
    q = torch.randn(B, H, S, D)
    k = torch.randn(B, H, S, D)
    v = torch.randn(B, H, S, D)
    out = gqa_attention(q, k, v)
    assert out.shape == (B, H, S, D)
    ref = _ref_attention(q, k, v)
    assert torch.allclose(out, ref, atol=1e-5)


def test_gqa_expands_kv():
    B, Hq, Hkv, D, S = 1, 8, 2, 4, 3
    q = torch.randn(B, Hq, S, D)
    k = torch.randn(B, Hkv, S, D)
    v = torch.randn(B, Hkv, S, D)
    ref = _ref_attention(q, k, v)
    out = gqa_attention(q, k, v)
    assert torch.allclose(out, ref, atol=1e-5)


def test_causal_does_not_see_future():
    B, H, D, S = 1, 2, 4, 4
    q = torch.randn(B, H, S, D)
    k = torch.randn(B, H, S, D)
    v = torch.randn(B, H, S, D)
    out1 = gqa_attention(q, k, v, causal=True)

    k2, v2 = k.clone(), v.clone()
    k2[:, :, 3:] = torch.randn(B, H, 1, D)
    v2[:, :, 3:] = torch.randn(B, H, 1, D)
    out2 = gqa_attention(q, k2, v2, causal=True)

    assert torch.allclose(out1[:, :, :3], out2[:, :, :3], atol=1e-6)
    assert not torch.allclose(out1[:, :, 3], out2[:, :, 3])


def test_padding_mask_ignores_masked_keys():
    B, Hq, Hkv, D, Sk = 1, 2, 2, 4, 4
    q = torch.randn(B, Hq, 1, D)
    k = torch.randn(B, Hkv, Sk, D)
    v = torch.randn(B, Hkv, Sk, D)

    key_ok = torch.tensor([[1, 1, 0, 0]])  # 2 key hợp lệ
    out = gqa_attention(q, k, v, attention_mask=key_ok)
    ref = _ref_attention(q, k[:, :, :2], v[:, :, :2])
    assert torch.allclose(out, ref, atol=1e-5)


def test_decode_attends_all_valid_keys():
    B, Hq, Hkv, D, Sk = 1, 4, 2, 8, 6
    q = torch.randn(B, Hq, 1, D)
    k = torch.randn(B, Hkv, Sk, D)
    v = torch.randn(B, Hkv, Sk, D)
    out = gqa_attention(q, k, v, attention_mask=torch.ones(B, Sk), causal=False)
    ref = _ref_attention(q, k, v)
    assert torch.allclose(out, ref, atol=1e-5)
