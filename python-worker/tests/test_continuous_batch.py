"""Tests cho continuous batching engine (CPU-only với fake model).

Kiểm tra: nhiều request/kết thúc đúng, parity greedy với reference per-sequence,
continuous admission giữa chừng, budget, cancel, stop_sequences.
"""

import asyncio
import time

import pytest
import torch

from fake_model import FakeHFModel, FakeTokenizer, decoder_factory
from worker.continuous_batch_engine import ContinuousBatchEngine


class _FakeHF:
    def apply_chat_template(self, formatted, tools=None, tokenize=False, add_generation_prompt=False):
        return " ".join(m.get("content", "") for m in formatted)


class _SlowModel(FakeHFModel):
    def __init__(self, delay=0.02, **kw):
        super().__init__(**kw)
        self.delay = delay

    def __call__(self, *a, **kw):
        time.sleep(self.delay)
        return super().__call__(*a, **kw)


def _engine(model=None, **kw):
    return ContinuousBatchEngine(
        model if model is not None else FakeHFModel(vocab=64),
        FakeTokenizer(),
        _FakeHF(),
        decoder_factory=decoder_factory,
        **kw,
    )


@pytest.fixture
def engines():
    made = []

    def factory(model=None, **kw):
        e = _engine(model=model, **kw)
        made.append(e)
        return e

    yield factory
    for e in made:
        e.stop()


def req(rid, text, max_tokens=5):
    return {
        "request_id": rid,
        "messages": [{"role": "user", "content": text}],
        "sampling_params": {"max_tokens": max_tokens, "temperature": 0.0},
    }


def reference_generate(model, prompt_ids, max_tokens):
    """Reference per-sequence (dùng cache của model, không assembly batch)."""
    ids = prompt_ids.unsqueeze(0)
    attn = torch.ones_like(ids)
    past = None
    out = []
    for _ in range(max_tokens):
        o = model(input_ids=ids, attention_mask=attn, past_key_values=past, use_cache=True)
        past = o.past_key_values
        tid = int(o.logits[0, -1, :].argmax().item())
        out.append(tid)
        ids = torch.tensor([[tid]])
        attn = torch.cat([attn, torch.ones(1, 1, dtype=torch.long)], dim=1)
    return out


# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_two_requests_complete(engines):
    eng = engines()
    got = {}
    async for rid, ev in eng.generate_batch([req("a", "hello", 3), req("b", "world", 4)]):
        got.setdefault(rid, []).append(ev)

    assert [e["type"] for e in got["a"]] == ["token", "token", "token", "final"]
    assert got["a"][-1]["stop_reason"] == "STOP_MAX_TOKENS"
    assert got["a"][-1]["usage"]["completion_tokens"] == 3
    assert got["b"][-1]["usage"]["completion_tokens"] == 4
    assert got["b"][-1]["usage"]["prompt_tokens"] == len(FakeTokenizer().encode("world"))


@pytest.mark.asyncio
async def test_greedy_matches_reference(engines):
    model = FakeHFModel(vocab=64)
    eng = engines(model=model)
    prompt_ids = torch.tensor(FakeTokenizer().encode("abc"))
    ref = reference_generate(model, prompt_ids, 6)

    got = []
    async for _rid, ev in eng.generate_batch([req("a", "abc", 6)]):
        if ev["type"] == "token":
            got.append(ord(ev["token"]))
    assert got == ref


@pytest.mark.asyncio
async def test_max_tokens_zero(engines):
    eng = engines()
    events = [ev async for _rid, ev in eng.generate_batch([req("a", "hi", 0)])]
    assert len(events) == 1
    assert events[0]["type"] == "final" and events[0]["stop_reason"] == "STOP_MAX_TOKENS"
    assert events[0]["usage"]["completion_tokens"] == 0


@pytest.mark.asyncio
async def test_stop_id_terminates(engines):
    tok = FakeTokenizer()
    vocab = 64
    prompt_ids = torch.tensor(tok.encode("abc"))
    first = (int(prompt_ids.sum()) + 1) % vocab
    tok.eos_token_id = first  # token sinh đầu tiên là stop id

    eng = engines(model=FakeHFModel(vocab=vocab))
    eng.tokenizer = tok
    events = [ev async for _rid, ev in eng.generate_batch([req("a", "abc", 5)])]
    assert len(events) == 1
    assert events[0]["stop_reason"] == "STOP_END_TURN" and events[0]["usage"]["completion_tokens"] == 0


@pytest.mark.asyncio
async def test_stop_sequence_cuts(engines):
    tok = FakeTokenizer()
    vocab = 64
    prompt_ids = torch.tensor(tok.encode("abc"))
    c = (int(prompt_ids.sum()) + 1) % vocab

    request = req("a", "abc", 5)
    request["sampling_params"]["stop_sequences"] = [chr(c)]
    eng = engines()
    events = [ev async for _rid, ev in eng.generate_batch([request])]
    assert events[0]["type"] == "final" and events[0]["stop_reason"] == "STOP_END_TURN"


@pytest.mark.asyncio
async def test_continuous_admits_midflight(engines):
    eng = engines(model=_SlowModel(delay=0.02, vocab=64))
    order = []

    async def run_a():
        async for _rid, ev in eng.generate_batch([req("a", "aaaa", 20)]):
            order.append(("a", ev["type"]))

    async def run_b():
        await asyncio.sleep(0.1)
        async for _rid, ev in eng.generate_batch([req("b", "bbbb", 2)]):
            order.append(("b", ev["type"]))

    await asyncio.gather(run_a(), run_b())

    a_final = next(i for i, x in enumerate(order) if x == ("a", "final"))
    b_final = next(i for i, x in enumerate(order) if x == ("b", "final"))
    assert b_final < a_final, order


@pytest.mark.asyncio
async def test_cancel_event(engines):
    eng = engines(model=_SlowModel(delay=0.01, vocab=64))
    ev = asyncio.Event()
    request = req("a", "xxxxx", 100)
    request["cancel_event"] = ev

    events = []

    async def consume():
        async for _rid, e in eng.generate_batch([request]):
            events.append(e)

    task = asyncio.create_task(consume())
    await asyncio.sleep(0.1)
    ev.set()
    await asyncio.wait_for(task, timeout=3)
    assert events[-1]["stop_reason"] == "STOP_CANCELLED"


@pytest.mark.asyncio
async def test_max_batch_tokens_serializes(engines):
    # prompt 4 + max 4 = 8/request; budget 10 → chỉ 1 request được admit mỗi lần.
    eng = engines(max_batch_tokens=10, max_batch_size=4)
    order = []
    async for rid, ev in eng.generate_batch([req("a", "aaaa", 4), req("b", "bbbb", 4)]):
        if ev["type"] == "final":
            order.append(("final", rid))
        elif ev["type"] == "token":
            order.append(("token", rid))

    a_final = next(i for i, x in enumerate(order) if x == ("final", "a"))
    b_first_token = next(i for i, x in enumerate(order) if x == ("token", "b"))
    assert a_final < b_first_token, order
