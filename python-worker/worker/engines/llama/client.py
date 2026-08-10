"""LlamaClient — transport HTTP tới llama-server (/v1/chat/completions, OpenAI-compatible).

Thin transport: yield raw JSON object của từng dòng `data: {...}`; KHÔNG hiểu event semantics
(cái đó thuộc LlamaBackend). Chấp nhận `transport` để inject httpx.MockTransport trong test.
"""
import json

import httpx


class LlamaClient:
    def __init__(self, base_url="http://127.0.0.1:8081", transport=None):
        self.base_url = base_url
        self._transport = transport

    def _client(self):
        return httpx.AsyncClient(
            base_url=self.base_url,
            timeout=httpx.Timeout(300.0, connect=10.0),
            transport=self._transport,
        )

    async def chat_completions(self, body):
        async with self._client() as client:
            async with client.stream("POST", "/v1/chat/completions", json=body) as resp:
                if resp.status_code != 200:
                    text = await resp.aread()
                    raise RuntimeError(
                        f"llama-server HTTP {resp.status_code}: "
                        f"{text.decode(errors='replace')[:500]}"
                    )
                async for line in resp.aiter_lines():
                    line = line.strip()
                    if not line.startswith("data:"):
                        continue
                    payload = line[len("data:"):].strip()
                    if not payload or payload == "[DONE]":
                        continue
                    yield json.loads(payload)
