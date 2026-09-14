"""Tests cho KV cache tự quản (`worker/kv_cache.py`).

Toàn bộ CPU-only với tensor nhỏ: kiểm tra buffer per-sequence, append slot cuối,
growth theo bội số, và assembly batch nhiều độ dài (left-pad + position_ids).
"""

import torch

from worker.kv_cache import KVCache, KVCacheManager


def _layer(k_vals, v_vals=None):
    """Một layer cho batch row đơn: k/v shape [1, 1, S, 1]."""
    v_vals = k_vals if v_vals is None else v_vals
    k = torch.tensor(k_vals, dtype=torch.float32).view(1, 1, -1, 1)
    v = torch.tensor(v_vals, dtype=torch.float32).view(1, 1, -1, 1)
    return (k, v)


def _past(*layers):
    return tuple(layers)


# ---------------------------------------------------------------------------
# KVCache
# ---------------------------------------------------------------------------

def test_init_from_prefill_sets_length_and_view():
    c = KVCache()
    c.init_from_prefill(_past(_layer([1.0, 2.0, 3.0])), prompt_len=3)
    assert c.length == 3
    k, _ = c.view()[0]
    assert k.shape == (1, 1, 3, 1)
    assert k[0, 0, :, 0].tolist() == [1.0, 2.0, 3.0]


def test_append_takes_last_slot_and_grows_length():
    c = KVCache()
    c.init_from_prefill(_past(_layer([1.0, 2.0])), prompt_len=2)
    # HF trả cache dài thêm 1 slot; slot cuối là token vừa sinh.
    c.append_from_output(_past(_layer([1.0, 2.0, 9.0])), row=0)
    assert c.length == 3
    k, _ = c.view()[0]
    assert k[0, 0, -1, 0].item() == 9.0


def test_capacity_grows_and_view_trims():
    c = KVCache()
    c.init_from_prefill(_past(_layer([1.0])), prompt_len=1)
    for i in range(2, 6):  # length 2..5
        c.append_from_output(_past(_layer([float(x) for x in range(1, i + 1)])), row=0)
    assert c.length == 5
    assert c.capacity >= 5
    k, _ = c.view()[0]
    assert k.shape == (1, 1, 5, 1)  # view cắt theo length, không theo capacity
    assert k[0, 0, :, 0].tolist() == [1.0, 2.0, 3.0, 4.0, 5.0]


def test_init_from_prefill_extracts_row_last_slots():
    # Batch 2, left-pad: row 0 dài 3, row 1 dài 1 (pad ở đầu).
    k = torch.tensor([[1.0, 2.0, 3.0], [0.0, 0.0, 7.0]]).view(2, 1, 3, 1)
    c = KVCache()
    c.init_from_prefill(((k, k),), prompt_len=1, row=1)
    ck, _ = c.view()[0]
    assert c.length == 1
    assert ck[0, 0, :, 0].tolist() == [7.0]


def test_free_resets():
    c = KVCache()
    c.init_from_prefill(_past(_layer([1.0])), prompt_len=1)
    c.free()
    assert c.length == 0 and c.capacity == 0 and c.layers == []


# ---------------------------------------------------------------------------
# KVCacheManager
# ---------------------------------------------------------------------------

def test_build_prefill_left_pads_and_positions():
    mgr = KVCacheManager()
    si = mgr.build_prefill([torch.tensor([4, 5, 6]), torch.tensor([7])])
    assert si.input_ids.shape == (2, 3)
    assert si.attention_mask.tolist() == [[1, 1, 1], [0, 0, 1]]
    assert si.position_ids.tolist() == [[0, 1, 2], [0, 0, 0]]
    assert si.input_ids[1].tolist() == [0, 0, 7]
    assert si.past_key_values is None


def test_build_decode_assembles_padded_past_and_positions():
    mgr = KVCacheManager()
    c0 = KVCache()
    c0.init_from_prefill(_past(_layer([1.0, 2.0, 3.0])), prompt_len=3)
    c1 = KVCache()
    c1.init_from_prefill(_past(_layer([7.0])), prompt_len=1)

    si = mgr.build_decode([c0, c1], [torch.tensor([8]), torch.tensor([9])])
    assert si.input_ids.tolist() == [[8], [9]]
    # L = 3; row1 length 1 → mask [0,0,1] + token mới 1
    assert si.attention_mask.tolist() == [[1, 1, 1, 1], [0, 0, 1, 1]]
    assert si.position_ids.tolist() == [[3], [1]]

    k, v = si.past_key_values[0]
    assert k.shape == (2, 1, 3, 1) and v.shape == (2, 1, 3, 1)
    assert k[0, 0, :, 0].tolist() == [1.0, 2.0, 3.0]
    assert k[1, 0, :, 0].tolist() == [0.0, 0.0, 7.0]  # left-pad


def test_total_tokens():
    mgr = KVCacheManager()
    c0 = KVCache()
    c0.init_from_prefill(_past(_layer([1.0, 2.0, 3.0])), prompt_len=3)
    c1 = KVCache()
    c1.init_from_prefill(_past(_layer([7.0])), prompt_len=1)
    assert mgr.total_tokens([c0, c1]) == 4
