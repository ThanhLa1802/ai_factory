"""`build_chat_prompt` tạo đúng chuỗi từ chat template (format ổn định)."""

from worker.prompt import build_chat_prompt


class _FakeHF:
    def apply_chat_template(self, formatted, tools=None, tokenize=False, add_generation_prompt=False):
        parts = []
        for m in formatted:
            parts.append(m["role"] + ":" + str(m.get("content", "")))
            for tc in m.get("tool_calls", []):
                parts.append("call:" + tc["function"]["name"])
        if tools:
            parts.append("tools:" + str(len(tools)))
        return "|".join(parts)


MSGS = [
    {"role": "system", "content": "sys"},
    {"role": "user", "content": "hi"},
    {"role": "assistant", "content": "", "tool_calls": [{"id": "1", "name": "read_file", "arguments": "{}"}]},
    {"role": "tool", "tool_call_id": "1", "content": "data"},
]

TOOLS = [{"type": "function", "function": {"name": "read_file", "description": "d", "parameters": {}}}]


def test_build_chat_prompt_with_tools():
    got = build_chat_prompt(_FakeHF(), MSGS, TOOLS)
    assert got == "system:sys|user:hi|assistant:|call:read_file|tool:data|tools:1"


def test_build_chat_prompt_without_tools():
    got = build_chat_prompt(_FakeHF(), MSGS)
    assert got == "system:sys|user:hi|assistant:|call:read_file|tool:data"
