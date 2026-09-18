"""Tests cho `BlockPrefixCache` — chia sẻ block prefix theo refcount (CPU-only)."""

import torch

from worker.block_manager import BlockManager, BlockPrefixCache, PagedKVCache


def cache_with(m, vals):
    c = PagedKVCache(m)
    k = torch.tensor([float(v) for v in vals], dtype=torch.float32).view(1, 1, -1, 1)
    c.init_from_prefill(((k.clone(), k.clone()),), prompt_len=len(vals))
    return c


def make(bs=4, num_blocks=16, max_blocks=None):
    m = BlockManager(num_blocks, 1, 1, bs, 1)
    return m, BlockPrefixCache(m, block_size=bs, max_blocks=max_blocks)


def test_match_returns_blocks_and_increfs():
    m, pc = make()
    c = cache_with(m, [1, 2, 3, 4, 5, 6, 7, 8])
    pc.insert([1, 2, 3, 4, 5, 6, 7, 8], c)

    matched, ids = pc.match([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12])
    assert matched == 8
    assert ids == c.block_table[:2]
    assert m.refcount(ids[0]) == 2  # c + match
    assert m.refcount(ids[1]) == 2


def test_partial_match_stops_at_first_diff():
    m, pc = make()
    c = cache_with(m, [1, 2, 3, 4, 5, 6, 7, 8])
    pc.insert([1, 2, 3, 4, 5, 6, 7, 8], c)

    matched, ids = pc.match([1, 2, 3, 4, 9, 9, 9, 9, 1, 2, 3, 4])
    assert matched == 4 and len(ids) == 1


def test_odd_tail_not_cached():
    m, pc = make()
    c = cache_with(m, [1, 2, 3, 4, 5, 6])  # 1 block đủ + 2 token lẻ
    pc.insert([1, 2, 3, 4, 5, 6], c)
    assert len(pc) == 1


def test_hash_depends_on_parent():
    m, pc = make()
    c = cache_with(m, [1, 2, 3, 4, 5, 6, 7, 8])
    pc.insert([1, 2, 3, 4, 5, 6, 7, 8], c)

    # block đầu khác → block sau (cùng token) không được tái dùng.
    matched, _ = pc.match([9, 9, 9, 9, 5, 6, 7, 8, 1, 2, 3, 4])
    assert matched == 0


def test_short_prompt_leaves_one_token_to_prefill():
    m, pc = make()
    c = cache_with(m, [1, 2, 3, 4])
    pc.insert([1, 2, 3, 4], c)
    # n=4, bs=4 → max_full = ((4-1)//4)*4 = 0 → không match (chừa ≥1 token).
    matched, _ = pc.match([1, 2, 3, 4])
    assert matched == 0


def test_eviction_drops_mapping():
    m, pc = make(bs=2, num_blocks=2)
    c = cache_with(m, [1, 2, 3, 4])
    pc.insert([1, 2, 3, 4], c)
    assert len(pc) == 2
    c.free()  # 2 block về refcount 0
    m.alloc()
    m.alloc()  # tái dùng cả 2 → on_evict xoá mapping
    assert len(pc) == 0
    matched, _ = pc.match([1, 2, 3, 4, 5, 6])
    assert matched == 0


def test_max_blocks_limits_mapping():
    m = BlockManager(16, 1, 1, 2, 1)
    pc = BlockPrefixCache(m, block_size=2, max_blocks=1)
    c = cache_with(m, [1, 2, 3, 4])
    pc.insert([1, 2, 3, 4], c)
    assert len(pc) == 1
