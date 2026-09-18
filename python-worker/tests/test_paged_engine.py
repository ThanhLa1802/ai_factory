"""Engine-level tests cho PagedAttention (CPU-only, FakeHFModel)."""

import asyncio
import threading

from fake_model import FakeHFModel, FakeTokenizer, decoder_factory
from worker.block_manager import BlockManager, BlockPrefixCache
from worker.continuous_batch_engine import ContinuousBatchEngine
from worker.prefix_cache import PrefixCache


class _SpyModel(FakeHFModel):
    """FakeHFModel + ghi lại độ rộng `input_ids` mỗi forward (prefill/decode)."""

    def __init__(self):
        super().__init__()
        self.prefill_widths = []

    def __call__(self, input_ids, attention_mask=None, position_ids=None,
                 past_key_values=None, use_cache=True, **kwargs):
        width = int(input_ids.shape[1])
        if width > 1:
            self.prefill_widths.append(width)
        return super().__call__(
            input_ids, attention_mask=attention_mask, position_ids=position_ids,
            past_key_values=past_key_values, use_cache=use_cache, **kwargs,
        )


def _prompt_builder(hf, msgs, tools):
    return " ".join(m.get("content", "") for m in msgs)


def make_engine(model, prefix_cache=None, paged=False, block_manager=None):
    return ContinuousBatchEngine(
        model,
        FakeTokenizer(),
        hf_tokenizer=None,
        max_batch_size=4,
        device="cpu",
        decoder_factory=decoder_factory,
        prompt_builder=_prompt_builder,
        prefix_cache=prefix_cache,
        paged=paged,
        block_manager=block_manager,
    )


def make_paged(model, block_size=4, num_blocks=64, share=True):
    bm = BlockManager(num_blocks, model.num_layers, 1, block_size, 1)
    pc = BlockPrefixCache(bm, block_size=block_size) if share else None
    engine = make_engine(model, prefix_cache=pc, paged=True, block_manager=bm)
    return engine, bm, pc


async def run(engine, content, request_id, max_tokens=4):
    req = {
        "request_id": request_id,
        "messages": [{"role": "user", "content": content}],
        "sampling_params": {"max_tokens": max_tokens, "temperature": 0.0},
    }
    events = []
    async for _rid, ev in engine.generate_batch([req]):
        events.append(ev)
    return events


async def run_multi(engine, contents, max_tokens=4):
    reqs = [
        {
            "request_id": f"r{i}",
            "messages": [{"role": "user", "content": c}],
            "sampling_params": {"max_tokens": max_tokens, "temperature": 0.0},
        }
        for i, c in enumerate(contents)
    ]
    out = {}
    async for rid, ev in engine.generate_batch(reqs):
        out.setdefault(rid, []).append(ev)
    return out


def tokens(events):
    return [e["token"] for e in events if e["type"] == "token"]


def final(events):
    return [e for e in events if e["type"] == "final"][0]


def test_paged_parity_without_prefix_cache():
    content = "abcdefghijklmnopqrst"

    plain = _SpyModel()
    off = asyncio.run(run(make_engine(plain), content, "r1"))

    paged_model = _SpyModel()
    engine, _bm, _pc = make_paged(paged_model, share=False)
    on = asyncio.run(run(engine, content, "r1"))

    assert tokens(off) == tokens(on)
    assert final(off)["stop_reason"] == final(on)["stop_reason"]
    assert final(off)["usage"] == final(on)["usage"]


def test_paged_parity_with_prefix_cache():
    content = "abcdefghijklmnopqrst"

    off_model = _SpyModel()
    off_engine = make_engine(off_model, prefix_cache=PrefixCache(block_size=4))
    off1 = asyncio.run(run(off_engine, content, "r1"))
    off2 = asyncio.run(run(off_engine, content + "XY", "r2"))

    on_model = _SpyModel()
    on_engine, _bm, _pc = make_paged(on_model, block_size=4)
    on1 = asyncio.run(run(on_engine, content, "r1"))
    on2 = asyncio.run(run(on_engine, content + "XY", "r2"))

    for a, b in ((off1, on1), (off2, on2)):
        assert tokens(a) == tokens(b)
        assert final(a)["usage"] == final(b)["usage"]


def test_paged_prefix_hit_prefills_only_suffix():
    model = _SpyModel()
    engine, _bm, _pc = make_paged(model, block_size=4)
    asyncio.run(run(engine, "abcdefghijklmnopqrst", "r1"))  # 20 token, nạp cache
    model.prefill_widths.clear()

    asyncio.run(run(engine, "abcdefghijklmnopqrstXY", "r2"))  # 20 prefix + 2 suffix
    assert model.prefill_widths == [2]


def test_paged_multi_sequence_parity():
    contents = ["abcdefghij", "abcdZZZZZZZZZZ", "ab"]

    off_model = _SpyModel()
    off = asyncio.run(run_multi(make_engine(off_model), contents))

    on_model = _SpyModel()
    on_engine, _bm, _pc = make_paged(on_model, block_size=4)
    on = asyncio.run(run_multi(on_engine, contents))

    for rid in off:
        assert tokens(off[rid]) == tokens(on[rid])
        assert final(off[rid])["usage"] == final(on[rid])["usage"]


def test_paged_prefix_does_not_leak_blocks():
    model = _SpyModel()
    engine, bm, _pc = make_paged(model, block_size=4)
    asyncio.run(run(engine, "abcdefghijklmnopqrst", "r1"))
    asyncio.run(run(engine, "abcdefghijklmnopqrstXY", "r2"))  # prefix hit
    assert bm.used_count() == 0
    assert bm.free_count() == bm.num_blocks


def test_paged_cancel_before_prefill_releases_prefix_refs():
    model = _SpyModel()
    engine, bm, _pc = make_paged(model, block_size=4)
    asyncio.run(run(engine, "abcdefghijklmnopqrst", "r1"))

    cancel = threading.Event()
    cancel.set()

    async def drain():
        req = {
            "request_id": "r2",
            "messages": [{"role": "user", "content": "abcdefghijklmnopqrstXY"}],
            "sampling_params": {"max_tokens": 4, "temperature": 0.0},
            "cancel_event": cancel,
        }
        return [e async for _rid, e in engine.generate_batch([req])]

    events = asyncio.run(drain())
    assert any(e["finish_reason"] == "cancelled" for e in events)
    assert bm.used_count() == 0
    assert bm.free_count() == bm.num_blocks
