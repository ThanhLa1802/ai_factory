"""LlamaClient — transport HTTP tới llama-server (/v1/chat/completions, OpenAI-compatible).

Thin transport: yield raw JSON object của từng dòng `data: {...}`; KHÔNG hiểu event semantics
(cái đó thuộc LlamaBackend). Chấp nhận `transport` để inject httpx.MockTransport trong test.
"""
from ..openai_compat import OpenAICompatClient


class LlamaClient(OpenAICompatClient):
    def __init__(self, base_url="http://127.0.0.1:8081", transport=None):
        super().__init__(base_url=base_url, transport=transport, label="llama-server")
