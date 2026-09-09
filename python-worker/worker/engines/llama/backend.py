"""LlamaBackend — map gRPC request ↔ OpenAI API; sinh event dict giống TransformersBackend."""
import asyncio
import sys

from ..base import EngineBackend
from .server import LlamaServer
from .client import LlamaClient

STOP_FINISH = {
    "stop": "STOP_END_TURN",
    "length": "STOP_MAX_TOKENS",
    "tool_calls": "STOP_TOOL_USE",
}


def to_openai_messages(messages):
    """Internal message dicts (từ _messages_from_proto) → OpenAI Chat Completions format."""
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


def build_openai_request(messages, sampling_params, tools=None):
    body = {
        "model": "qwen3.5-9b",
        "messages": to_openai_messages(messages),
        "max_tokens": sampling_params.get("max_tokens", 1024),
        "temperature": sampling_params.get("temperature", 0.7),
        "top_p": sampling_params.get("top_p", 0.9),
        "top_k": sampling_params.get("top_k", 50),
        "stream": True,
        # llama.cpp chỉ trả `usage` trong stream khi bật cờ này; thiếu nó thì
        # prompt/completion tokens luôn = 0 → usage meter ghi sai.
        "stream_options": {"include_usage": True},
    }
    if sampling_params.get("stop_sequences"):
        body["stop"] = list(sampling_params["stop_sequences"])
    if tools:
        body["tools"] = tools
        body["tool_choice"] = "auto"
    return body


class LlamaBackend(EngineBackend):
    def __init__(self, gguf, port=8081, bin="llama-server", gpu_layers=-1):
        self.server = LlamaServer(gguf, port=port, bin=bin, gpu_layers=gpu_layers)
        self.client = LlamaClient(base_url=self.server.base_url)

    def load(self):
        self.server.start()

    def unload(self):
        self.server.stop()

    async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
        body = build_openai_request(messages, sampling_params, tools)
        tool_acc = {}
        usage = {}
        stop_reason = None
        finish_reason = None
        async for chunk in self.client.chat_completions(body):
            if cancel_event and cancel_event.is_set():
                return
            # llama.cpp trả usage ở CHUNK CUỐI (choices rỗng), SAU chunk finish_reason.
            # Phải tích luỹ dần thay vì return ngay ở finish_reason.
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
               "finish_reason": finish_reason, "usage": _usage_dict(usage)}

    def generate_batch(self, requests):
        async def _gen():
            q = asyncio.Queue()

            async def worker(req):
                try:
                    msgs = list(req.get("messages", []))
                    if req.get("system_prompt"):
                        msgs = [{"role": "system", "content": req["system_prompt"]}] + msgs
                    async for ev in self.generate(
                        msgs, req.get("sampling_params", {}), req.get("tools")
                    ):
                        await q.put((req.get("request_id", ""), ev))
                except Exception as e:  # noqa: BLE001
                    req_id = req.get("request_id", "")
                    # stderr là unbuffered → log không bị mất như stdout block-buffered.
                    print(f"[llama_batch] req {req_id} error: {e}", file=sys.stderr)
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

        return _gen()


def _usage_dict(u):
    return {
        "prompt_tokens": u.get("prompt_tokens", 0),
        "completion_tokens": u.get("completion_tokens", 0),
        "total_tokens": u.get("total_tokens", 0),
    }
