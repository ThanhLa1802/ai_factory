import httpx
import pytest

from worker.engines.llama.client import LlamaClient

SSE = (
    'data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}\n\n'
    'data: {"choices":[{"delta":{"content":" world"},"finish_reason":null}]}\n\n'
    'data: {"choices":[{"delta":{},"finish_reason":"stop"}],'
    '"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}\n\n'
    'data: [DONE]\n\n'
)


@pytest.mark.asyncio
async def test_parses_sse_chunks():
    def handler(request):
        return httpx.Response(200, content=SSE,
                              headers={"content-type": "text/event-stream"})

    client = LlamaClient(base_url="http://test",
                         transport=httpx.MockTransport(handler))
    chunks = [c async for c in client.chat_completions({})]
    assert len(chunks) == 3
    assert chunks[0]["choices"][0]["delta"]["content"] == "Hello"
    assert chunks[2]["choices"][0]["finish_reason"] == "stop"
    assert chunks[2]["usage"]["prompt_tokens"] == 5


@pytest.mark.asyncio
async def test_http_error_raises_runtime_error():
    def handler(request):
        return httpx.Response(500, content=b"boom")

    client = LlamaClient(base_url="http://test",
                         transport=httpx.MockTransport(handler))
    with pytest.raises(RuntimeError, match="500"):
        async for _ in client.chat_completions({}):
            pass
