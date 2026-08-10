import pytest

from worker.engines.llama.backend import (
    LlamaBackend, build_openai_request, to_openai_messages,
)


class _FakeClient:
    def __init__(self, chunks):
        self._chunks = chunks

    async def chat_completions(self, body):
        for c in self._chunks:
            yield c


def test_to_openai_messages():
    msgs = [
        {"role": "user", "content": "hi"},
        {"role": "assistant", "content": "", "tool_calls": [
            {"id": "c1", "name": "read_file", "arguments": '{"path":"a.txt"}'}]},
        {"role": "tool", "tool_call_id": "c1", "content": "OK"},
    ]
    out = to_openai_messages(msgs)
    assert out[1]["tool_calls"][0]["function"]["name"] == "read_file"
    assert out[2]["role"] == "tool" and out[2]["tool_call_id"] == "c1"


def test_build_openai_request():
    body = build_openai_request(
        [{"role": "user", "content": "hi"}],
        {"max_tokens": 10, "stop_sequences": ["<|im_end|>"]},
        tools=[{"type": "function", "function": {"name": "f"}}],
    )
    assert body["tools"] and body["tool_choice"] == "auto"
    assert body["stop"] == ["<|im_end|>"]
    assert body["stream"] is True


@pytest.mark.asyncio
async def test_generate_streams_tokens_and_final():
    chunks = [
        {"choices": [{"delta": {"content": "Hel"}, "finish_reason": None}]},
        {"choices": [{"delta": {"content": "lo"}, "finish_reason": None}]},
        {"choices": [{"delta": {}, "finish_reason": "stop"}],
         "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}},
    ]
    backend = LlamaBackend.__new__(LlamaBackend)
    backend.client = _FakeClient(chunks)
    events = [ev async for ev in backend.generate([], {})]
    assert events[0] == {"type": "token", "token": "Hel"}
    assert events[1] == {"type": "token", "token": "lo"}
    assert events[2]["type"] == "final"
    assert events[2]["stop_reason"] == "STOP_END_TURN"


@pytest.mark.asyncio
async def test_generate_tool_calls_accumulated():
    chunks = [
        {"choices": [{"delta": {"tool_calls": [
            {"index": 0, "id": "call_1", "type": "function",
             "function": {"name": "read_file", "arguments": '{"pat'}}]},
            "finish_reason": None}]},
        {"choices": [{"delta": {"tool_calls": [
            {"index": 0, "function": {"arguments": 'h":"a.txt"}'}}]},
            "finish_reason": None}]},
        {"choices": [{"delta": {}, "finish_reason": "tool_calls"}]},
    ]
    backend = LlamaBackend.__new__(LlamaBackend)
    backend.client = _FakeClient(chunks)
    events = [ev async for ev in backend.generate([], {})]
    tool = events[0]
    assert tool["type"] == "tool_use" and tool["name"] == "read_file"
    assert tool["arguments"] == '{"path":"a.txt"}'
    assert events[1]["stop_reason"] == "STOP_TOOL_USE"


@pytest.mark.asyncio
async def test_generate_batch_interleaves_and_completes():
    class B(LlamaBackend):
        def __init__(self):
            self.server = None
            self.client = None

        async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
            yield {"type": "token", "token": "x"}
            yield {"type": "final", "stop_reason": "STOP_END_TURN",
                   "finish_reason": "stop", "usage": {}}

    backend = B()
    reqs = [
        {"request_id": "a", "messages": [{"role": "user", "content": "1"}],
         "system_prompt": "", "sampling_params": {}, "tools": None},
        {"request_id": "b", "messages": [{"role": "user", "content": "2"}],
         "system_prompt": "", "sampling_params": {}, "tools": None},
    ]
    pairs = [(rid, ev["type"]) async for rid, ev in backend.generate_batch(reqs)]
    assert len(pairs) == 4
    assert {rid for rid, t in pairs if t == "final"} == {"a", "b"}
