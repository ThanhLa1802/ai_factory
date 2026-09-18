"""Tests cho `worker/block_manager.py` — pool block + block table + CoW (CPU-only)."""

import torch
import pytest

from worker.block_manager import BlockManager, PagedKVCache, OutOfBlocks


def past_from(values_per_layer):
    """Legacy-tuple `[(k, v)]` mỗi layer từ list giá trị theo vị trí."""
    layers = []
    for vals in values_per_layer:
        k = torch.tensor([float(v) for v in vals], dtype=torch.float32).view(1, 1, -1, 1)
        layers.append((k.clone(), k.clone()))
    return tuple(layers)


# ---------------------------------------------------------------------------
# BlockManager
# ---------------------------------------------------------------------------

def test_alloc_refcount_and_free_lru():
    m = BlockManager(3, 1, 1, 4, 1)
    assert m.free_count() == 3
    a = m.alloc()
    assert a == 0 and m.refcount(0) == 1 and m.free_count() == 2
    assert m.alloc() == 1
    m.incref(0)
    assert m.refcount(0) == 2
    m.decref(0)
    assert m.refcount(0) == 1
    m.decref(0)
    assert m.refcount(0) == 0 and m.free_count() == 2
    # free LRU: block 2 (lâu nhất) được tái dùng trước block 0 vừa nhả.
    assert m.alloc() == 2
    assert m.alloc() == 0


def test_out_of_blocks_raises():
    m = BlockManager(1, 1, 1, 4, 1)
    m.alloc()
    with pytest.raises(OutOfBlocks):
        m.alloc()


def test_on_evict_called_on_reuse():
    seen = []
    m = BlockManager(2, 1, 1, 4, 1)
    m.set_on_evict(seen.append)
    b0 = m.alloc()  # 0 (free LRU)
    m.decref(b0)
    assert m.alloc() == 1  # 1 lâu nhất trong free
    assert seen == [0, 1]


def test_gather_concatenates_blocks_and_trims_tail():
    m = BlockManager(8, 1, 1, 4, 1)
    c = PagedKVCache(m)
    c.init_from_prefill(past_from([[1, 2, 3, 4, 5, 6]]), prompt_len=6)
    assert c.length == 6
    assert c.capacity == 8  # 2 block
    k, v = c.view()[0]
    assert k.shape == (1, 1, 6, 1) and v.shape == (1, 1, 6, 1)
    assert k[0, 0, :, 0].tolist() == [1, 2, 3, 4, 5, 6]


def test_append_crosses_block_boundary():
    m = BlockManager(8, 1, 1, 4, 1)
    c = PagedKVCache(m)
    c.init_from_prefill(past_from([[1, 2, 3, 4]]), prompt_len=4)
    assert len(c.block_table) == 1
    c.append_from_output(past_from([[1, 2, 3, 4, 9]]), row=0)
    assert c.length == 5 and len(c.block_table) == 2
    k, _ = c.view()[0]
    assert k[0, 0, :, 0].tolist() == [1, 2, 3, 4, 9]


# ---------------------------------------------------------------------------
# Chia sẻ / copy-on-write
# ---------------------------------------------------------------------------

def test_adopt_shares_blocks_without_copy():
    m = BlockManager(8, 1, 1, 4, 1)
    src = PagedKVCache(m)
    src.init_from_prefill(past_from([[1, 2, 3, 4]]), prompt_len=4)
    bid = src.block_table[0]
    dst = PagedKVCache(m)
    dst.adopt([bid])
    assert dst.length == 4 and dst.block_table == [bid]
    assert m.refcount(bid) == 2
    k, _ = dst.view()[0]
    assert k[0, 0, :, 0].tolist() == [1, 2, 3, 4]


def test_fork_then_append_triggers_copy_on_write():
    m = BlockManager(8, 1, 1, 4, 1)
    parent = PagedKVCache(m)
    parent.init_from_prefill(past_from([[1, 2, 3]]), prompt_len=3)  # block lẻ
    child = parent.fork()
    shared = parent.block_table[0]
    assert m.refcount(shared) == 2

    child.append_from_output(past_from([[1, 2, 3, 9]]), row=0)
    assert child.length == 4
    assert m.refcount(shared) == 1
    assert child.block_table[0] != shared  # CoW đã tách
    ck, _ = child.view()[0]
    pk, _ = parent.view()[0]
    assert ck[0, 0, :, 0].tolist() == [1, 2, 3, 9]
    assert pk[0, 0, :, 0].tolist() == [1, 2, 3]


def test_init_after_adopt_writes_only_suffix():
    m = BlockManager(8, 1, 1, 4, 1)
    src = PagedKVCache(m)
    src.init_from_prefill(past_from([[1, 2, 3, 4]]), prompt_len=4)
    prefix_bid = src.block_table[0]

    dst = PagedKVCache(m)
    dst.adopt([prefix_bid])
    # forward trả full P=6 (prefix + suffix 7,8)
    dst.init_from_prefill(past_from([[1, 2, 3, 4, 7, 8]]), prompt_len=6)
    assert dst.length == 6
    assert dst.block_table[0] == prefix_bid  # prefix còn chia sẻ
    assert len(dst.block_table) == 2
    assert m.refcount(prefix_bid) == 2
    k, _ = dst.view()[0]
    assert k[0, 0, :, 0].tolist() == [1, 2, 3, 4, 7, 8]


def test_free_returns_all_blocks():
    m = BlockManager(8, 1, 1, 4, 1)
    c = PagedKVCache(m)
    c.init_from_prefill(past_from([[1, 2, 3, 4, 5]]), prompt_len=5)
    assert m.used_count() == 2
    c.free()
    assert c.length == 0 and c.block_table == []
    assert m.used_count() == 0 and m.free_count() == 8
