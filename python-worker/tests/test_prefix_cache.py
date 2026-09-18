"""Tests cho PrefixCache (CPU-only, thuần tensor)."""

import torch

from worker.prefix_cache import PrefixCache

BS = 4


def make_layers(token_ids):
    """Layers giả: kênh 0 của K mã hoá token id để kiểm tra nội dung block."""
    L = len(token_ids)
    k = torch.zeros(1, 2, L, 2)
    v = torch.zeros(1, 2, L, 2)
    for i, t in enumerate(token_ids):
        k[0, :, i, 0] = t
        v[0, :, i, 0] = -t
    return [(k, v)]


def tokens_of(layers, length):
    k = layers[0][0]
    return [int(k[0, 0, i, 0]) for i in range(length)]


def test_empty_cache_miss():
    cache = PrefixCache(block_size=BS)
    assert cache.match([1, 2, 3, 4]) == (0, None)


def test_insert_and_match_two_blocks():
    cache = PrefixCache(block_size=BS)
    toks = [1, 2, 3, 4, 5, 6, 7, 8]
    cache.insert(toks, make_layers(toks), length=8)
    assert len(cache) == 2

    matched, layers = cache.match(toks)
    assert matched == 8
    assert layers[0][0].shape == (1, 2, 8, 2)
    assert tokens_of(layers, 8) == toks


def test_partial_mismatch_stops_at_first_differing_block():
    cache = PrefixCache(block_size=BS)
    cache.insert([1, 2, 3, 4, 5, 6, 7, 8], make_layers([1, 2, 3, 4, 5, 6, 7, 8]), length=8)

    matched, layers = cache.match([1, 2, 3, 4, 9, 9, 9, 9])
    assert matched == 4
    assert tokens_of(layers, 4) == [1, 2, 3, 4]


def test_short_and_odd_prompts_are_not_cached():
    cache = PrefixCache(block_size=BS)
    cache.insert([1, 2, 3], make_layers([1, 2, 3]), length=3)
    assert len(cache) == 0

    cache.insert([1, 2, 3, 4, 5, 6], make_layers([1, 2, 3, 4, 5, 6]), length=6)
    assert len(cache) == 1
    matched, _ = cache.match([1, 2, 3, 4, 5, 6])
    assert matched == 4


def test_hash_chain_isolates_blocks_by_parent():
    cache = PrefixCache(block_size=BS)
    cache.insert([1, 2, 3, 4], make_layers([1, 2, 3, 4]), length=4)
    assert len(cache) == 1

    # Cùng độ dài, block 0 khác → block 1 (nếu có) phải có khoá khác.
    cache.insert([9, 9, 9, 9, 5, 6, 7, 8], make_layers([9, 9, 9, 9, 5, 6, 7, 8]), length=8)
    matched, layers = cache.match([1, 2, 3, 4, 5, 6, 7, 8])
    assert matched == 4  # block [5,6,7,8] không khớp vì parent khác
    assert tokens_of(layers, 4) == [1, 2, 3, 4]


def test_lru_eviction():
    cache = PrefixCache(block_size=BS, max_blocks=1)
    cache.insert([1, 2, 3, 4], make_layers([1, 2, 3, 4]), length=4)
    cache.insert([9, 9, 9, 9], make_layers([9, 9, 9, 9]), length=4)
    assert len(cache) == 1
    assert cache.match([1, 2, 3, 4])[0] == 0
    assert cache.match([9, 9, 9, 9])[0] == 4


def test_lru_recency_on_match():
    cache = PrefixCache(block_size=BS, max_blocks=2)
    cache.insert([1, 2, 3, 4], make_layers([1, 2, 3, 4]), length=4)
    cache.insert([5, 6, 7, 8], make_layers([5, 6, 7, 8]), length=4)
    cache.match([1, 2, 3, 4])  # block 1 trở thành mới nhất
    cache.insert([9, 9, 9, 9], make_layers([9, 9, 9, 9]), length=4)

    assert cache.match([1, 2, 3, 4])[0] == 4
    assert cache.match([5, 6, 7, 8])[0] == 0


def test_insert_respects_length():
    cache = PrefixCache(block_size=BS)
    toks = [1, 2, 3, 4, 5, 6, 7, 8]
    cache.insert(toks, make_layers(toks), length=6)
    assert len(cache) == 1
    assert cache.match(toks)[0] == 4
