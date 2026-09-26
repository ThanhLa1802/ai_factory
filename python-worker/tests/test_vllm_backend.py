import asyncio

import pytest

from worker.engines.vllm.backend import VLLMBackend


class _FakeClient:
    def __init__(self, chunks):
        self._chunks = chunks

    async def chat_completions(self, body):
        for c in self._chunks:
            yield c


def _stub_backend(chunks, model="m"):
    """VLLMBackend không qua `__init__` — gán tay client giả + `_gen_lock`."""
    backend = VLLMBackend.__new__(VLLMBackend)
    backend.model = model
    backend.client = _FakeClient(chunks)
    backend._gen_lock = asyncio.Lock()
    return backend


def test_spawn_mode_builds_local_server():
    backend = VLLMBackend(model="Qwen/Qwen2.5-1.5B-Instruct", port=8123)
    assert backend.server is not None
    assert backend.client.base_url == "http://127.0.0.1:8123"


def test_remote_mode_skips_spawn():
    backend = VLLMBackend(model="my-model", url="http://vllm:8000/")
    assert backend.server is None
    assert backend.client.base_url == "http://vllm:8000"
    # load/unload phải no-op (không spawn/stop process nào).
    backend.load()
    backend.unload()


@pytest.mark.asyncio
async def test_build_request_uses_model():
    backend = _stub_backend([], model="served-name")
    body = backend.build_request([{"role": "user", "content": "hi"}], {})
    assert body["model"] == "served-name"
    assert body["stream"] is True


@pytest.mark.asyncio
async def test_generate_streams_tokens_and_final():
    chunks = [
        {"choices": [{"delta": {"content": "Hel"}, "finish_reason": None}]},
        {"choices": [{"delta": {"content": "lo"}, "finish_reason": None}]},
        {"choices": [{"delta": {}, "finish_reason": "stop"}],
         "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}},
    ]
    backend = _stub_backend(chunks)
    events = [ev async for ev in backend.generate([], {})]
    assert events[0] == {"type": "token", "token": "Hel"}
    assert events[1] == {"type": "token", "token": "lo"}
    assert events[2]["type"] == "final"
    assert events[2]["stop_reason"] == "STOP_END_TURN"
    assert events[2]["usage"] == {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}


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
    backend = _stub_backend(chunks)
    events = [ev async for ev in backend.generate([], {})]
    assert events[0]["type"] == "tool_use"
    assert events[0]["name"] == "read_file"
    assert events[0]["arguments"] == '{"path":"a.txt"}'
    assert events[1]["stop_reason"] == "STOP_TOOL_USE"


@pytest.mark.asyncio
async def test_generate_defaults_when_stream_ends_without_finish():
    backend = _stub_backend([{"choices": [{"delta": {"content": "x"}, "finish_reason": None}]}])
    events = [ev async for ev in backend.generate([], {})]
    assert events[-1]["type"] == "final"
    assert events[-1]["stop_reason"] == "STOP_END_TURN"
    assert events[-1]["finish_reason"] == "stop"
