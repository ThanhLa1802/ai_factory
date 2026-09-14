"""Chat prompt builder dùng chung — HF Jinja chat template (quyết định D1).

Tách từ `BatchEngine._build_prompt` để continuous engine và engine cũ dùng chung
một định dạng prompt. Token hoá vẫn do BPETokenizer tự viết (`worker/model/tokenizer`).
"""

from typing import Optional


def build_chat_prompt(hf_tokenizer, messages: list[dict], tools: Optional[list[dict]] = None) -> str:
    """Build prompt từ messages (internal canonical) bằng `apply_chat_template`.

    Fallback sang template không có `tools` nếu model template không hỗ trợ.
    """
    formatted = []
    for msg in messages:
        role = msg.get("role", "user")
        content = msg.get("content", "")

        if role == "tool":
            formatted.append({
                "role": "tool",
                "tool_call_id": msg.get("tool_call_id", ""),
                "content": content,
            })
            continue

        fm = {"role": role, "content": content}
        if msg.get("tool_calls"):
            fm["tool_calls"] = [
                {
                    "id": tc["id"],
                    "type": "function",
                    "function": {
                        "name": tc["name"],
                        "arguments": tc.get("arguments", "{}"),
                    },
                }
                for tc in msg["tool_calls"]
            ]
        formatted.append(fm)

    try:
        return hf_tokenizer.apply_chat_template(
            formatted, tools=tools, tokenize=False, add_generation_prompt=True
        )
    except Exception:
        return hf_tokenizer.apply_chat_template(
            formatted, tokenize=False, add_generation_prompt=True
        )
