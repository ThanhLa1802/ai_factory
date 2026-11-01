import pytest

from worker.pb import inference_pb2
from worker.server import (
    InferenceServicer,
    _build_batch_response,
    _build_response,
    _messages_from_proto,
)


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


# ---------------------------------------------------------------------------
# image input (proto → engine dict, text-engine guard)
# ---------------------------------------------------------------------------

def test_messages_from_proto_carries_images():
    msgs = [
        inference_pb2.Message(
            role="user",
            content="what is this?",
            images=[
                inference_pb2.Image(mime_type="image/png", data=b"\x89PNG-1"),
                inference_pb2.Image(mime_type="image/jpeg", data=b"\xff\xd8-2"),
            ],
        )
    ]
    out = _messages_from_proto(msgs)
    assert out[0]["content"] == "what is this?"
    assert out[0]["images"] == [b"\x89PNG-1", b"\xff\xd8-2"]
    assert out[0]["image_mime_types"] == ["image/png", "image/jpeg"]


def test_messages_from_proto_omits_images_when_absent():
    out = _messages_from_proto([inference_pb2.Message(role="user", content="hi")])
    assert "images" not in out[0]


class _RejectContext:
    def __init__(self):
        self.written = []

    def cancelled(self):
        return False

    async def write(self, resp):
        self.written.append(resp)


@pytest.mark.asyncio
async def test_text_engine_rejects_image_input():
    class TextBackend:
        # No supports_vision attribute: inherits the text-engine default.
        async def generate(self, **kwargs):  # noqa: ARG002
            raise AssertionError("a text engine must not be asked to run image input")
            yield  # pragma: no cover — makes this an async generator

    req = inference_pb2.GenerateRequest(
        request_id="r1",
        messages=[
            inference_pb2.Message(
                role="user",
                content="describe",
                images=[inference_pb2.Image(mime_type="image/png", data=b"png")],
            )
        ],
    )
    ctx = _RejectContext()
    await InferenceServicer(TextBackend()).Generate(req, ctx)

    assert len(ctx.written) == 1
    final = ctx.written[0]
    assert final.event_type == inference_pb2.EVENT_FINAL
    assert final.stop_reason == inference_pb2.STOP_ERROR
    assert final.token == "image input requires --engine vl"
