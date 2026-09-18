"""Engine-level tests cho prefix caching (CPU-only, FakeHFModel)."""

import asyncio

from fake_model import FakeHFModel, FakeTokenizer, decoder_factory
from worker.continuous_batch_engine import ContinuousBatchEngine
from worker.prefix_cache import PrefixCache


class _SpyModel(FakeHFModel):
    """FakeHFModel + ghi lại độ rộng `input_ids` mỗi forward (prefill/decode)."""

    def __init__(self):
        super().__init__()
        self.calls = []
        self.prefill_widths = []

    def __call__(self, input_ids, attention_mask=None, position_ids=None,
                 past_key_values=None, use_cache=True, **kwargs):
        width = int(input_ids.shape[1])
        self.calls.append(width)
        # Prefill = forward nhiều hơn 1 token (decode luôn 1 token).
        if width > 1:
            self.prefill_widths.append(width)
        return super().__call__(
            input_ids, attention_mask=attention_mask, position_ids=position_ids,
            past_key_values=past_key_values, use_cache=use_cache, **kwargs,
        )


def _prompt_builder(hf, msgs, tools):
    return " ".join(m.get("content", "") for m in msgs)


def make_engine(model, prefix_cache=None):
    return ContinuousBatchEngine(
        model,
        FakeTokenizer(),
        hf_tokenizer=None,
        max_batch_size=4,
        device="cpu",
        decoder_factory=decoder_factory,
        prompt_builder=_prompt_builder,
        prefix_cache=prefix_cache,
    )


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


def tokens(events):
    return [e["token"] for e in events if e["type"] == "token"]


def final(events):
    return [e for e in events if e["type"] == "final"][0]


def test_engine_without_cache_prefills_full_prompt():
    model = _SpyModel()
    engine = make_engine(model, prefix_cache=None)
    asyncio.run(run(engine, "abcdefghijklmnopqrst", "r1"))
    assert model.prefill_widths == [20]


def test_prefix_hit_prefills_only_suffix():
    model = _SpyModel()
    cache = PrefixCache(block_size=4)
    engine = make_engine(model, prefix_cache=cache)
    asyncio.run(run(engine, "abcdefghijklmnopqrst", "r1"))  # 20 token, nạp cache
    model.prefill_widths.clear()

    asyncio.run(run(engine, "abcdefghijklmnopqrstXY", "r2"))  # 20 prefix + 2 suffix
    assert model.prefill_widths == [2]


def test_prefix_cache_parity_without_cache():
    content = "abcdefghijklmnopqrst"

    off = _SpyModel()
    engine_off = make_engine(off, prefix_cache=None)
    off1 = asyncio.run(run(engine_off, content, "r1"))
    off2 = asyncio.run(run(engine_off, content, "r2"))

    on = _SpyModel()
    engine_on = make_engine(on, prefix_cache=PrefixCache(block_size=4))
    on1 = asyncio.run(run(engine_on, content, "r1"))
    on2 = asyncio.run(run(engine_on, content, "r2"))

    for a, b in ((off1, on1), (off2, on2)):
        assert tokens(a) == tokens(b)
        assert final(a)["stop_reason"] == final(b)["stop_reason"]
        assert final(a)["usage"] == final(b)["usage"]


def test_partial_prefix_only_prefills_suffix():
    model = _SpyModel()
    cache = PrefixCache(block_size=4)
    engine = make_engine(model, prefix_cache=cache)
    asyncio.run(run(engine, "abcdefghij", "r1"))  # 10 token -> 2 block đủ (8)
    model.prefill_widths.clear()

    asyncio.run(run(engine, "abcdefghijZZ", "r2"))  # khớp 8, suffix 4
    assert model.prefill_widths == [4]
