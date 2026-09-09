import pytest

from worker.server import InferenceServicer, _build_response, _build_batch_response


@pytest.mark.asyncio
async def test_inference_servicer_streams_backend_events():
    class FakeBackend:
        async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
            yield {"type": "token", "token": "Hi"}
            yield {"type": "final", "stop_reason": "STOP_END_TURN",
                   "finish_reason": "stop", "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}

    class FakeContext:
        def __init__(self):
            self.written = []

        def cancelled(self):
            return False

        async def write(self, resp):
            self.written.append(resp)

    req = type("Req", (), {
        "request_id": "r1", "session_id": "s1",
        "messages": [], "tools": [], "system_prompt": "",
        "sampling_params": type("SP", (), {
            "max_tokens": 0, "temperature": 0, "top_p": 0, "top_k": 0, "stop_sequences": [],
        })(),
    })()

    fake = FakeBackend()
    ctx = FakeContext()
    svc = InferenceServicer(fake)
    await svc.Generate(req, ctx)
    assert svc.backend is fake  # old servicer sets self.engine → AttributeError
    assert [r.event_type for r in ctx.written] == [1, 3]  # EVENT_TOKEN=1, EVENT_FINAL=3
    assert ctx.written[-1].stop_reason == 1  # STOP_END_TURN


def test_build_response_token():
    resp = _build_response({"type": "token", "token": "x"})
    assert resp.event_type == 1 and resp.token == "x"


def test_build_response_reasoning():
    resp = _build_response({"type": "reasoning", "token": "th"})
    assert resp.event_type == 4 and resp.reasoning_token == "th"  # EVENT_REASONING=4


def test_build_batch_response_reasoning():
    resp = _build_batch_response("r1", {"type": "reasoning", "token": "th"})
    assert resp.request_id == "r1"
    assert resp.event_type == 4 and resp.reasoning_token == "th"
