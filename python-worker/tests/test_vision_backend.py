"""CPU tests for the Qwen2.5-VL engine (no model download, no GPU).

The real model needs ~7GB VRAM, so these tests cover the pure mapping helpers
and the streaming/event contract with a fake processor + model.
"""

import base64
import io

import pytest
import torch
from PIL import Image

from worker.engines.vision import (
    VisionBackend,
    build_hf_messages,
    decode_images,
    sampling_kwargs,
)
from worker.server import _messages_have_images


def _png_bytes(color=(255, 0, 0)) -> bytes:
    buf = io.BytesIO()
    Image.new("RGB", (8, 8), color).save(buf, format="PNG")
    return buf.getvalue()


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

def test_decode_images_from_base64_and_bytes():
    raw = _png_bytes()
    images = decode_images([raw, base64.b64encode(raw).decode()])
    assert len(images) == 2
    assert all(img.mode == "RGB" and img.size == (8, 8) for img in images)


def test_build_hf_messages_puts_image_before_text():
    raw = _png_bytes()
    hf, images = build_hf_messages([
        {"role": "system", "content": "be nice"},
        {"role": "user", "content": "what is this?", "images": [raw]},
    ])
    assert hf[0] == {"role": "system", "content": "be nice"}
    parts = hf[1]["content"]
    assert [p["type"] for p in parts] == ["image", "text"]
    assert parts[1]["text"] == "what is this?"
    assert len(images) == 1


def test_build_hf_messages_maps_tool_role():
    hf, images = build_hf_messages([
        {"role": "tool", "tool_result": "42", "content": ""},
    ])
    assert images == []
    assert hf[0]["role"] == "user"
    assert "Tool result:" in hf[0]["content"] and "42" in hf[0]["content"]


def test_sampling_kwargs_greedy_and_stochastic():
    greedy = sampling_kwargs({"max_tokens": 10, "temperature": 0.0})
    assert greedy == {"max_new_tokens": 10, "do_sample": False}

    sampled = sampling_kwargs({"max_tokens": 20, "temperature": 0.7, "top_p": 0.9, "top_k": 40})
    assert sampled["do_sample"] is True
    assert sampled["max_new_tokens"] == 20
    assert sampled["temperature"] == pytest.approx(0.7)
    assert sampled["top_p"] == pytest.approx(0.9)
    assert sampled["top_k"] == 40


def test_messages_have_images():
    assert not _messages_have_images([{"role": "user", "content": "hi"}])
    assert _messages_have_images([{"role": "user", "content": "", "images": [b"x"]}])


# ---------------------------------------------------------------------------
# streaming contract (fake processor/model)
# ---------------------------------------------------------------------------

class _FakeInputs(dict):
    def to(self, device):  # noqa: ARG002
        return self


class _FakeTokenizer:
    def __call__(self, text, add_special_tokens=False):  # noqa: ARG002
        return {"input_ids": list(range(len(text)))}


class _FakeProcessor:
    tokenizer = _FakeTokenizer()

    def apply_chat_template(self, messages, tokenize=False, add_generation_prompt=True):  # noqa: ARG002
        return "PROMPT"

    def __call__(self, text=None, images=None, return_tensors=None):  # noqa: ARG002
        return _FakeInputs(input_ids=torch.zeros(1, 7, dtype=torch.long))


class _FakeStreamer:
    chunks = ["Hello", " world"]

    def __init__(self, tokenizer, skip_prompt=True, skip_special_tokens=True):  # noqa: ARG002
        pass

    def __iter__(self):
        return iter(self.chunks)

    def end(self):
        pass


class _FakeModel:
    device = "cpu"

    def generate(self, **kwargs):  # noqa: ARG002
        return None


def _backend() -> VisionBackend:
    b = VisionBackend()
    b.model = _FakeModel()
    b.processor = _FakeProcessor()
    b._TextIteratorStreamer = _FakeStreamer
    return b


def _req(**overrides):
    req = {
        "request_id": "r1",
        "messages": [{"role": "user", "content": "hi"}],
        "sampling_params": {"max_tokens": 1024, "temperature": 0.0},
    }
    req.update(overrides)
    return req


@pytest.mark.asyncio
async def test_generate_streams_tokens_and_usage():
    b = _backend()
    events = [ev async for _rid, ev in b.generate_batch([_req()])]

    streamed = "".join(e["token"] for e in events if e["type"] == "token")
    assert streamed == "Hello world"

    final = events[-1]
    assert final["type"] == "final"
    assert final["stop_reason"] == "STOP_END_TURN"
    assert final["finish_reason"] == "stop"
    assert final["usage"]["prompt_tokens"] == 7
    assert final["usage"]["completion_tokens"] == len("Hello world")


@pytest.mark.asyncio
async def test_generate_reports_cancellation():
    import asyncio

    b = _backend()
    cancel = asyncio.Event()
    cancel.set()
    events = [ev async for _rid, ev in b.generate_batch([_req(cancel_event=cancel)])]

    assert not [e for e in events if e["type"] == "token"]
    assert events[-1]["stop_reason"] == "STOP_CANCELLED"
    assert events[-1]["finish_reason"] == "cancelled"


@pytest.mark.asyncio
async def test_generate_without_model_is_an_error_event():
    b = VisionBackend()
    events = [ev async for _rid, ev in b.generate_batch([_req()])]
    assert events[-1]["stop_reason"] == "STOP_ERROR"
    assert "not loaded" in events[-1]["token"]


def test_load_keeps_lm_head_in_bf16_and_ties_weights(monkeypatch):
    """Qwen2.5-VL checkpoints tie lm_head to embed_tokens (no lm_head.weight).

    Quantising lm_head to 4-bit and then tying bf16 embeddings into it breaks
    bitsandbytes' quant state, so lm_head must be skipped from quantisation and
    the tie must happen explicitly after load.
    """
    import transformers

    # transformers is a lazy module: touching each attribute first forces the
    # real class to be imported and cached, so monkeypatch.setattr below is not
    # overwritten by the lazy import.
    for _name in (
        "Qwen2_5_VLForConditionalGeneration",
        "AutoProcessor",
        "BitsAndBytesConfig",
        "TextIteratorStreamer",
    ):
        getattr(transformers, _name)

    captured = {}
    class _FakeModel:
        def tie_weights(self):
            captured["tied"] = True

        def eval(self):
            pass

    class _FakeQwen:
        @staticmethod
        def from_pretrained(model_id, **kwargs):
            captured["model_id"] = model_id
            captured["kwargs"] = kwargs
            return _FakeModel()

    class _FakeProcessor:
        @staticmethod
        def from_pretrained(model_id, **kwargs):
            return object()

    def _fake_bnb(**kwargs):
        captured["quant"] = kwargs
        return object()

    monkeypatch.setattr("transformers.Qwen2_5_VLForConditionalGeneration", _FakeQwen)
    monkeypatch.setattr("transformers.AutoProcessor", _FakeProcessor)
    monkeypatch.setattr("transformers.BitsAndBytesConfig", _fake_bnb)
    monkeypatch.setattr("transformers.TextIteratorStreamer", object)
    monkeypatch.setattr(torch.cuda, "is_available", lambda: True)

    VisionBackend(model_id="fake/vl").load()

    assert captured["tied"] is True
    assert "lm_head" in captured["quant"]["llm_int8_skip_modules"]
    assert "visual" in captured["quant"]["llm_int8_skip_modules"]
