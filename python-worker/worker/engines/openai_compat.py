"""Shared OpenAI-compatible HTTP plumbing for proxy engines (llama.cpp, vLLM).

Both engines expose an OpenAI Chat Completions API with SSE streaming, so the
message mapping, request body, stream parsing and engine-event mapping live here
once. Each engine package supplies only its server launcher + served model name.
"""
import asyncio
import json
import sys

import httpx

from .base import EngineBackend

STOP_FINISH = {
    "stop": "STOP_END_TURN",
    "length": "STOP_MAX_TOKENS",
    "tool_calls": "STOP_TOOL_USE",
}


def to_openai_messages(messages):
    """Internal message dicts (from _messages_from_proto) → OpenAI Chat format."""
    out = []
    for m in messages:
        role = m.get("role", "user")
        if role == "tool":
            out.append({
                "role": "tool",
                "tool_call_id": m.get("tool_call_id", ""),
                "content": m.get("content", ""),
            })
            continue
        fm = {"role": role, "content": m.get("content", "")}
        if m.get("tool_calls"):
            fm["tool_calls"] = [
                {"id": tc["id"], "type": "function",
                 "function": {"name": tc["name"], "arguments": tc.get("arguments", "{}")}}
                for tc in m["tool_calls"]
            ]
        out.append(fm)
    return out


def build_openai_request(messages, sampling_params, tools=None, model="model"):
    body = {
        "model": model,
        "messages": to_openai_messages(messages),
        "max_tokens": sampling_params.get("max_tokens", 1024),
        "temperature": sampling_params.get("temperature", 0.7),
        "top_p": sampling_params.get("top_p", 0.9),
        "top_k": sampling_params.get("top_k", 50),
        "stream": True,
        # llama.cpp / vLLM only emit `usage` in the stream when this is set;
        # without it prompt/completion tokens are 0 and the usage meter is wrong.
        "stream_options": {"include_usage": True},
    }
    if sampling_params.get("stop_sequences"):
        body["stop"] = list(sampling_params["stop_sequences"])
    if tools:
        body["tools"] = tools
        body["tool_choice"] = "auto"
    return body


def usage_dict(u):
    return {
        "prompt_tokens": u.get("prompt_tokens", 0),
        "completion_tokens": u.get("completion_tokens", 0),
        "total_tokens": u.get("total_tokens", 0),
    }


class OpenAICompatClient:
    """Thin SSE transport to `/v1/chat/completions` (OpenAI-compatible).

    Yields the raw JSON object of each `data: {...}` line; it does NOT understand
    event semantics (that belongs to the backend). `transport` allows injecting
    httpx.MockTransport in tests; `label` only names the server in error messages.
    """

    def __init__(self, base_url="http://127.0.0.1:8080", transport=None, label="engine"):
        self.base_url = base_url
        self._transport = transport
        self._label = label

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
                        f"{self._label} HTTP {resp.status_code}: "
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


class OpenAICompatBackend(EngineBackend):
    """EngineBackend for any OpenAI-compatible server (llama-server, vLLM).

    Subclasses set `self.client` and `self.model` in `__init__`; the streaming
    and batch plumbing is shared. `model` is a class attribute default so
    stubbed backends (tests) work without `__init__`.
    """

    model = "model"

    def build_request(self, messages, sampling_params, tools=None):
        return build_openai_request(messages, sampling_params, tools, model=self.model)

    async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
        async for event in self._guard(self._generate(messages, sampling_params, tools, cancel_event)):
            yield event

    async def _generate(self, messages, sampling_params, tools=None, cancel_event=None):
        body = self.build_request(messages, sampling_params, tools)
        tool_acc = {}
        usage = {}
        stop_reason = None
        finish_reason = None
        async for chunk in self.client.chat_completions(body):
            if cancel_event and cancel_event.is_set():
                return
            # Usage arrives in the FINAL chunk (empty choices), AFTER the
            # finish_reason chunk — accumulate instead of returning early.
            usage = chunk.get("usage") or usage
            choices = chunk.get("choices") or []
            if not choices:
                continue
            delta = choices[0].get("delta") or {}
            if delta.get("reasoning_content"):
                yield {"type": "reasoning", "token": delta["reasoning_content"]}
            if delta.get("content"):
                yield {"type": "token", "token": delta["content"]}
            for tc in delta.get("tool_calls") or []:
                idx = tc.get("index", 0)
                slot = tool_acc.setdefault(idx, {"id": "", "name": "", "arguments": ""})
                if tc.get("id"):
                    slot["id"] = tc["id"]
                fn = tc.get("function") or {}
                if fn.get("name"):
                    slot["name"] = fn["name"]
                if fn.get("arguments"):
                    slot["arguments"] += fn["arguments"]
            finish = choices[0].get("finish_reason")
            if finish and finish_reason is None:
                finish_reason = finish
                stop_reason = STOP_FINISH.get(finish, "STOP_END_TURN")
                if stop_reason == "STOP_TOOL_USE":
                    for idx in sorted(tool_acc):
                        s = tool_acc[idx]
                        yield {"type": "tool_use", "id": s["id"],
                               "name": s["name"], "arguments": s["arguments"]}
        if stop_reason is None:
            stop_reason = "STOP_END_TURN"
        if finish_reason is None:
            finish_reason = "stop"
        yield {"type": "final", "stop_reason": stop_reason,
               "finish_reason": finish_reason, "usage": usage_dict(usage)}

    def generate_batch(self, requests):
        async def _gen():
            q = asyncio.Queue()

            async def worker(req):
                try:
                    msgs = list(req.get("messages", []))
                    if req.get("system_prompt"):
                        msgs = [{"role": "system", "content": req["system_prompt"]}] + msgs
                    async for ev in self._generate(
                        msgs, req.get("sampling_params", {}), req.get("tools")
                    ):
                        await q.put((req.get("request_id", ""), ev))
                except Exception as e:  # noqa: BLE001
                    req_id = req.get("request_id", "")
                    # stderr is unbuffered → log survives unlike block-buffered stdout.
                    print(f"[{self.model} batch] req {req_id} error: {e}", file=sys.stderr)
                    await q.put((req_id, {
                        "type": "final", "stop_reason": "STOP_ERROR",
                        "finish_reason": "error", "token": str(e),
                    }))

            tasks = [asyncio.create_task(worker(r)) for r in requests]
            remaining = len(tasks)
            while remaining > 0:
                req_id, event = await q.get()
                yield req_id, event
                if event.get("type") == "final":
                    remaining -= 1
            for t in tasks:
                await t

        # Guarded as one unit: workers call the unguarded _generate, so the lock
        # is acquired once for the whole batch.
        return self._guard(_gen())
