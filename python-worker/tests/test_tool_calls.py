"""Tests cho tool-call detection trên đường transformers (continuous batch).

llama.cpp trả tool_calls có cấu trúc; đường transformers phải tự parse text
`<tool_call>{...}</tool_call>`. Test dùng model "scripted" phát ra sẵn chuỗi token.
"""

import asyncio

import pytest
import torch

from fake_model import FakeOutput, FakeTokenizer, decoder_factory
from worker.continuous_batch_engine import ContinuousBatchEngine
from worker.tool_calls import marker_holdback, parse_tool_calls


class _FakeHF:
    def apply_chat_template(self, formatted, tools=None, tokenize=False, add_generation_prompt=False):
        return " ".join(m.get("content", "") for m in formatted)


class ScriptedModel:
    """Phát ra đúng chuỗi token trong `script` (mỗi lần gọi model = 1 token)."""

    def __init__(self, script, vocab=1024, num_layers=1):
        self.script = list(script)
        self.vocab = vocab
        self.num_layers = num_layers
        self.calls = 0

    def __call__(self, input_ids, attention_mask=None, position_ids=None,
                 past_key_values=None, use_cache=True, **kwargs):
        B, q = input_ids.shape
        logits = torch.full((B, q, self.vocab), -20.0)
        nxt = self.script[self.calls] if self.calls < len(self.script) else self.script[-1]
        logits[:, -1, nxt] = 20.0
        self.calls += 1

        new_ids = input_ids.float().view(B, 1, q, 1)
        if past_key_values is None:
            past = tuple((new_ids.clone(), new_ids.clone()) for _ in range(self.num_layers))
        else:
            past = tuple(
                (torch.cat([k, new_ids], dim=2), torch.cat([v, new_ids], dim=2))
                for k, v in past_key_values
            )
        return FakeOutput(logits, past)


def _script(text: str) -> list:
    return [ord(ch) for ch in text] + [FakeTokenizer.eos_token_id]


def _engine(model):
    return ContinuousBatchEngine(
        model, FakeTokenizer(), _FakeHF(), decoder_factory=decoder_factory
    )


def _req(rid="a", text="go", max_tokens=200):
    return {
        "request_id": rid,
        "messages": [{"role": "user", "content": text}],
        "sampling_params": {"max_tokens": max_tokens, "temperature": 0.0},
    }


async def _run(eng, request):
    return [ev async for _rid, ev in eng.generate_batch([request])]


# ---------------------------------------------------------------------------
# parse_tool_calls (unit)
# ---------------------------------------------------------------------------

def test_parse_single_call_object_arguments():
    calls = parse_tool_calls('<tool_call>{"name":"read_file","arguments":{"path":"a.txt"}}</tool_call>')
    assert len(calls) == 1
    assert calls[0]["name"] == "read_file"
    assert calls[0]["arguments"] == '{"path": "a.txt"}'
    assert calls[0]["id"].startswith("call_")


def test_parse_arguments_string_kept_as_is():
    calls = parse_tool_calls('<tool_call>{"name":"run","arguments":"{\\"cmd\\":\\"ls\\"}"}</tool_call>')
    assert calls[0]["arguments"] == '{"cmd":"ls"}'


def test_parse_multiple_calls():
    text = ('<tool_call>{"name":"a","arguments":{}}</tool_call>'
            '<tool_call>{"name":"b","arguments":{"x":1}}</tool_call>')
    calls = parse_tool_calls(text)
    assert [c["name"] for c in calls] == ["a", "b"]


def test_parse_skips_broken_and_nameless():
    assert parse_tool_calls("<tool_call>{not json}</tool_call>") == []
    assert parse_tool_calls('<tool_call>{"arguments":{}}</tool_call>') == []
    assert parse_tool_calls("no call here") == []


def test_marker_holdback():
    assert marker_holdback("hello") == 0
    assert marker_holdback("a<") == 1
    assert marker_holdback("a<tool_ca") == len("<tool_ca")
    assert marker_holdback("<tool_call>") == 0  # marker đã đủ → không giữ lại


# ---------------------------------------------------------------------------
# engine integration
# ---------------------------------------------------------------------------

@pytest.mark.asyncio
async def test_batch_path_emits_tool_use():
    text = '<tool_call>{"name": "read_file", "arguments": {"path": "a.txt"}}</tool_call>'
    eng = _engine(ScriptedModel(_script(text)))
    try:
        events = await _run(eng, _req())
    finally:
        eng.stop()

    tool_events = [e for e in events if e["type"] == "tool_use"]
    assert len(tool_events) == 1
    assert tool_events[0]["name"] == "read_file"
    assert tool_events[0]["arguments"] == '{"path": "a.txt"}'

    final = events[-1]
    assert final["type"] == "final"
    assert final["stop_reason"] == "STOP_TOOL_USE" and final["finish_reason"] == "tool_use"


@pytest.mark.asyncio
async def test_markup_not_streamed_as_content():
    text = 'thinking<tool_call>{"name": "read_file", "arguments": {"path": "a.txt"}}</tool_call>'
    eng = _engine(ScriptedModel(_script(text)))
    try:
        events = await _run(eng, _req())
    finally:
        eng.stop()

    streamed = "".join(e["token"] for e in events if e["type"] == "token")
    assert streamed == "thinking"
    assert "<tool_call>" not in streamed


@pytest.mark.asyncio
async def test_broken_tool_json_falls_back_to_end_turn():
    text = "<tool_call>{oops</tool_call>"
    eng = _engine(ScriptedModel(_script(text)))
    try:
        events = await _run(eng, _req())
    finally:
        eng.stop()

    assert not [e for e in events if e["type"] == "tool_use"]
    assert events[-1]["stop_reason"] == "STOP_END_TURN"


@pytest.mark.asyncio
async def test_plain_text_with_angle_bracket_is_flushed():
    eng = _engine(ScriptedModel(_script("a<b")))
    try:
        events = await _run(eng, _req())
    finally:
        eng.stop()

    streamed = "".join(e["token"] for e in events if e["type"] == "token")
    assert streamed == "a<b"
    assert events[-1]["stop_reason"] == "STOP_END_TURN"
