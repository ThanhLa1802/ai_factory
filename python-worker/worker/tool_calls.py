"""Tách tool call từ text sinh bởi model (đường transformers).

llama.cpp trả `tool_calls` có cấu trúc (LlamaBackend dùng trực tiếp), còn đường
transformers chỉ sinh text thô nên phải tự parse. Chat template của Qwen bọc mỗi
call trong `<tool_call> ... </tool_call>` với JSON `{"name": ..., "arguments": ...}`.
"""

import json
import uuid

TOOL_CALL_OPEN = "<tool_call>"
TOOL_CALL_CLOSE = "</tool_call>"


def marker_holdback(text: str) -> int:
    """Số ký tự cuối của `text` có thể là đầu của `TOOL_CALL_OPEN` (chưa đủ).

    Cho phép streaming giữ lại đúng phần đuôi có khả năng thành marker, nên
    markup `<tool_call>` không bị lộ ra content dù marker bị cắt qua nhiều token.
    """
    limit = min(len(text), len(TOOL_CALL_OPEN) - 1)
    for n in range(limit, 0, -1):
        if text.endswith(TOOL_CALL_OPEN[:n]):
            return n
    return 0


def parse_tool_calls(text: str) -> list[dict]:
    """Tách mọi `<tool_call>...</tool_call>` trong `text` thành event payload.

    `arguments` luôn là JSON string (proto `ToolUse.arguments` yêu cầu string).
    Call hỏng (JSON lỗi / thiếu `name`) bị bỏ qua; trả `[]` nếu không parse được
    call nào — khi đó request kết thúc bằng stop reason mặc định của engine.
    """
    calls: list[dict] = []
    marker = text.find(TOOL_CALL_OPEN)
    while marker != -1:
        start = marker + len(TOOL_CALL_OPEN)
        end = text.find(TOOL_CALL_CLOSE, start)
        raw = text[start:end] if end != -1 else text[start:]
        call = _parse_one(raw)
        if call is not None:
            calls.append(call)
        if end == -1:
            break
        marker = text.find(TOOL_CALL_OPEN, end + len(TOOL_CALL_CLOSE))
    return calls


def _parse_one(raw: str) -> dict | None:
    raw = raw.strip()
    if not raw:
        return None
    try:
        obj = json.loads(raw)
    except (json.JSONDecodeError, ValueError):
        return None
    if not isinstance(obj, dict):
        return None
    name = obj.get("name")
    if not name:
        return None
    args = obj.get("arguments", {})
    if not isinstance(args, str):
        args = json.dumps(args, ensure_ascii=False)
    return {"id": "call_" + uuid.uuid4().hex[:16], "name": name, "arguments": args}
