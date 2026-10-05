"""Tests cho `worker.image_server` (proto conversion + servicer, CPU-only).

Kiểm tra map proto <-> dict/backend và luồng unary/streaming mà không cần
diffusers hay GPU (backend giả).
"""

import asyncio

from worker.image_server import ImageServiceServicer, _req_from_proto, _response_from_result
from worker.pb import image_pb2


def test_req_from_proto_maps_all_fields():
    req = image_pb2.ImageRequest(
        request_id="r1", prompt="p", negative_prompt="n",
        width=8, height=6, steps=2, guidance_scale=3.5,
        seed=7, num_images=2, scheduler="euler",
    )
    d = _req_from_proto(req)

    assert d["request_id"] == "r1"
    assert d["prompt"] == "p"
    assert d["negative_prompt"] == "n"
    assert (d["width"], d["height"]) == (8, 6)
    assert d["steps"] == 2
    assert d["guidance_scale"] == 3.5
    assert d["seed"] == 7
    assert d["num_images"] == 2
    assert d["scheduler"] == "euler"


def test_response_from_result_maps_png_bytes():
    resp = _response_from_result({
        "png": [b"a", b"b"], "width": 8, "height": 6,
        "seed": 7, "steps": 2, "scheduler": "default",
    })

    assert list(resp.png) == [b"a", b"b"]
    assert (resp.width, resp.height) == (8, 6)
    assert resp.seed == 7
    assert resp.steps == 2
    assert resp.scheduler == "default"


class FakeContext:
    def __init__(self):
        self.written = []
        self._cancelled = False

    def cancelled(self):
        return self._cancelled

    async def write(self, message):
        self.written.append(message)

    async def abort(self, code, details):
        raise RuntimeError(details)


class FakeBackend:
    async def generate(self, req):
        return {"png": [b"x"], "width": 8, "height": 6, "seed": 1, "steps": 2,
                "scheduler": "default"}

    async def stream(self, req):
        yield {"step": 1, "total_steps": 2, "done": False}
        yield {"step": 2, "total_steps": 2, "done": True,
               "result": {"png": [b"x"], "width": 8, "height": 6, "seed": 1,
                          "steps": 2, "scheduler": "default"}}


def test_servicer_generate_image():
    svc = ImageServiceServicer(FakeBackend())
    resp = asyncio.run(svc.GenerateImage(image_pb2.ImageRequest(prompt="x"), FakeContext()))

    assert list(resp.png) == [b"x"]
    assert resp.width == 8


def test_servicer_stream_image_writes_progress_and_result():
    svc = ImageServiceServicer(FakeBackend())
    ctx = FakeContext()
    asyncio.run(svc.StreamImage(image_pb2.ImageRequest(prompt="x"), ctx))

    assert len(ctx.written) == 2
    assert ctx.written[0].done is False
    assert ctx.written[0].step == 1
    assert ctx.written[-1].done is True
    assert list(ctx.written[-1].result.png) == [b"x"]


class FailingBackend(FakeBackend):
    async def generate(self, req):
        raise RuntimeError("kaboom")


def test_servicer_generate_error_aborts():
    svc = ImageServiceServicer(FailingBackend())

    try:
        asyncio.run(svc.GenerateImage(image_pb2.ImageRequest(prompt="x"), FakeContext()))
    except RuntimeError as e:
        assert "kaboom" in str(e)
    else:  # pragma: no cover
        raise AssertionError("expected abort to raise")
