"""Parity tests cho forward pass tự viết (CPU-only, tiny Qwen2).

Baseline (Task 1): xác nhận harness tiny Qwen2 + cache của HF hoạt động đúng.
Task 4: self-forward (full + incremental + tương thích KV cache tự quản).
"""

import torch

from tiny_qwen import build_tiny_qwen
from worker.kv_cache import KVCache, KVCacheManager
from worker.model.forward import Qwen2Forward
from worker.model.hf_forward import HFForwardAdapter


def test_hf_forward_baseline_shape():
    model = build_tiny_qwen()
    ids = torch.tensor([[1, 2, 3, 4, 5]])
    with torch.no_grad():
        out = model(ids)
    assert out.logits.shape == (1, 5, model.config.vocab_size)


def test_hf_cached_decode_matches_full_forward():
    """Prefill + decode từng bước (position_ids/attention_mask tường minh)
    phải khớp full forward — xác nhận harness dùng đúng cache."""
    model = build_tiny_qwen()
    ids = torch.tensor([[5, 6, 7, 8, 9, 10, 11]])
    prefill_len = 3

    with torch.no_grad():
        full = model(ids).logits

        out = model(
            ids[:, :prefill_len],
            attention_mask=torch.ones(1, prefill_len, dtype=torch.long),
            position_ids=torch.arange(prefill_len).unsqueeze(0),
            use_cache=True,
        )
        past = out.past_key_values

        stepped = []
        for i in range(prefill_len, ids.shape[1]):
            attn = torch.ones(1, i + 1, dtype=torch.long)
            pos = torch.tensor([[i]])
            out = model(
                ids[:, i : i + 1],
                attention_mask=attn,
                position_ids=pos,
                past_key_values=past,
                use_cache=True,
            )
            past = out.past_key_values
            stepped.append(out.logits[:, -1, :])

    stepped = torch.stack(stepped, dim=1)
    assert torch.allclose(stepped, full[:, prefill_len:, :], atol=1e-4, rtol=1e-3)


def test_self_forward_full_matches_hf():
    model = build_tiny_qwen()
    fwd = Qwen2Forward(model)
    ids = torch.tensor([[1, 2, 3, 4, 5, 6, 7]])
    with torch.no_grad():
        ref = model(ids).logits
        out = fwd(ids)
    assert torch.allclose(out.logits, ref, atol=1e-4, rtol=1e-3)


def test_self_forward_defaults_without_mask_or_positions():
    model = build_tiny_qwen()
    fwd = Qwen2Forward(model)
    ids = torch.tensor([[3, 1, 4, 1, 5]])
    with torch.no_grad():
        ref = model(ids).logits
        out = fwd(ids, attention_mask=None, position_ids=None)
    assert torch.allclose(out.logits, ref, atol=1e-4, rtol=1e-3)


def test_self_forward_incremental_matches_hf_full():
    model = build_tiny_qwen()
    fwd = Qwen2Forward(model)
    ids = torch.tensor([[5, 6, 7, 8, 9, 10, 11]])
    prefill_len = 3

    with torch.no_grad():
        ref = model(ids).logits

        out = fwd(
            ids[:, :prefill_len],
            attention_mask=torch.ones(1, prefill_len, dtype=torch.long),
            position_ids=torch.arange(prefill_len).unsqueeze(0),
            use_cache=True,
        )
        past = out.past_key_values

        stepped = []
        for i in range(prefill_len, ids.shape[1]):
            out = fwd(
                ids[:, i : i + 1],
                attention_mask=torch.ones(1, i + 1, dtype=torch.long),
                position_ids=torch.tensor([[i]]),
                past_key_values=past,
                use_cache=True,
            )
            past = out.past_key_values
            stepped.append(out.logits[:, -1, :])

    stepped = torch.stack(stepped, dim=1)
    assert torch.allclose(stepped, ref[:, prefill_len:, :], atol=1e-4, rtol=1e-3)


def test_self_forward_cache_has_gqa_kv_heads():
    model = build_tiny_qwen()
    fwd = Qwen2Forward(model)
    ids = torch.tensor([[1, 2, 3]])
    with torch.no_grad():
        out = fwd(ids)
    assert len(out.past_key_values) == model.config.num_hidden_layers
    for k, v in out.past_key_values:
        assert k.shape == (1, model.config.num_key_value_heads, 3, fwd.head_dim)
        assert v.shape == (1, model.config.num_key_value_heads, 3, fwd.head_dim)
    assert model.config.num_key_value_heads < model.config.num_attention_heads


def test_self_forward_accepts_padded_decode_past():
    """Tương thích KVCache/KVCacheManager: prefill batch nhiều độ dài rồi decode."""
    model = build_tiny_qwen()
    fwd = Qwen2Forward(model)
    prompts = [torch.tensor([1, 2, 3]), torch.tensor([4, 5, 6, 7, 8])]
    pending = [torch.tensor([11]), torch.tensor([12])]
    mgr = KVCacheManager()

    with torch.no_grad():
        si = mgr.build_prefill(prompts)
        out = fwd(
            si.input_ids,
            attention_mask=si.attention_mask,
            position_ids=si.position_ids,
            use_cache=True,
        )
        caches = [KVCache() for _ in prompts]
        for i, p in enumerate(prompts):
            caches[i].init_from_prefill(out.past_key_values, int(p.shape[0]), row=i)

        si2 = mgr.build_decode(caches, pending)
        out2 = fwd(
            si2.input_ids,
            attention_mask=si2.attention_mask,
            position_ids=si2.position_ids,
            past_key_values=si2.past_key_values,
            use_cache=True,
        )

        for i, (p, tid) in enumerate(zip(prompts, pending)):
            full = torch.cat([p, tid]).unsqueeze(0)
            ref = model(full).logits[:, -1, :]
            assert torch.allclose(out2.logits[i], ref[0], atol=1e-4, rtol=1e-3)


def test_hf_adapter_accepts_legacy_tuple_and_matches_full_forward():
    """Đường rollback (AI_FACTORY_SELF_FORWARD=0): adapter chuyển tuple <-> Cache."""
    model = build_tiny_qwen()
    adapter = HFForwardAdapter(model)
    ids = torch.tensor([[5, 6, 7, 8, 9, 10, 11]])
    prefill_len = 3

    with torch.no_grad():
        ref = model(ids).logits

        out = adapter(
            ids[:, :prefill_len],
            attention_mask=torch.ones(1, prefill_len, dtype=torch.long),
            position_ids=torch.arange(prefill_len).unsqueeze(0),
            use_cache=True,
        )
        past = out.past_key_values
        assert isinstance(past, tuple)

        stepped = []
        for i in range(prefill_len, ids.shape[1]):
            out = adapter(
                ids[:, i : i + 1],
                attention_mask=torch.ones(1, i + 1, dtype=torch.long),
                position_ids=torch.tensor([[i]]),
                past_key_values=past,
                use_cache=True,
            )
            past = out.past_key_values
            stepped.append(out.logits[:, -1, :])

    stepped = torch.stack(stepped, dim=1)
    assert torch.allclose(stepped, ref[:, prefill_len:, :], atol=1e-4, rtol=1e-3)
