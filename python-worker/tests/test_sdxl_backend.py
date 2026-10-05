"""Tests cho `worker.engines.sdxl.SDXLBackend` (CPU-only, pipeline giả).

Cổng chính: backend chạy đúng pipeline và truyền đúng tham số (size, steps,
guidance, negative prompt, seed/n, progress callback), encode PNG hợp lệ, và
stream phát tiến độ + kết quả/lỗi. Không cần GPU, không tải model.
"""

import asyncio
import io

import pytest
import torch
from PIL import Image

from fake_image import FakePipeline
from worker.engines.sdxl import TURBO_MODEL_ID, SDXLBackend


async def _collect(ait):
    return [item async for item in ait]


def make_backend(pipe: FakePipeline, **kwargs) -> SDXLBackend:
    backend = SDXLBackend(pipeline_factory=lambda: pipe, device="cpu", **kwargs)
    backend.load()
    return backend


def test_load_configures_pipeline():
    pipe = FakePipeline()
    backend = make_backend(pipe, vae_tiling=True)

    assert backend.pipe is pipe
    assert pipe.vae_tiling is True
    assert pipe.progress_disabled is True
    assert pipe.to_device == torch.device("cpu")
    assert pipe.cpu_offload is False


def test_load_cpu_offload_preferred_over_to():
    pipe = FakePipeline()
    make_backend(pipe, cpu_offload=True)

    assert pipe.cpu_offload is True
    assert pipe.to_device is None  # có offload thì không .to(device)


def test_generate_returns_valid_png_with_size():
    pipe = FakePipeline()
    backend = make_backend(pipe)

    result = asyncio.run(backend.generate({
        "prompt": "a cat", "width": 64, "height": 48,
        "steps": 3, "num_images": 2, "seed": 42,
    }))

    assert len(result["png"]) == 2
    assert result["seed"] == 42
    assert result["steps"] == 3
    assert (result["width"], result["height"]) == (64, 48)
    img = Image.open(io.BytesIO(result["png"][0]))
    assert img.size == (64, 48)
    assert img.format == "PNG"


def test_seed_passed_and_random_when_negative():
    pipe = FakePipeline()
    backend = make_backend(pipe)

    asyncio.run(backend.generate({"prompt": "x", "seed": 123}))
    assert pipe.calls[-1]["generator"].initial_seed() == 123

    random_result = asyncio.run(backend.generate({"prompt": "x", "seed": -1}))
    assert random_result["seed"] >= 0


def test_negative_prompt_and_guidance_passed():
    pipe = FakePipeline()
    backend = make_backend(pipe)

    asyncio.run(backend.generate({
        "prompt": "x", "negative_prompt": "blurry", "guidance_scale": 3.5,
    }))
    call = pipe.calls[-1]
    assert call["negative_prompt"] == "blurry"
    assert call["guidance_scale"] == pytest.approx(3.5)

    asyncio.run(backend.generate({"prompt": "x", "negative_prompt": ""}))
    assert pipe.calls[-1]["negative_prompt"] is None


def test_turbo_variant_defaults():
    pipe = FakePipeline()
    backend = make_backend(pipe, variant="turbo")

    assert backend.model_id == TURBO_MODEL_ID
    result = asyncio.run(backend.generate({"prompt": "x"}))
    assert result["steps"] == 4
    assert pipe.calls[-1]["guidance_scale"] == pytest.approx(0.0)


def test_request_overrides_defaults():
    pipe = FakePipeline()
    backend = make_backend(pipe)

    result = asyncio.run(backend.generate({"prompt": "x", "steps": 10, "guidance_scale": 2.0}))
    assert result["steps"] == 10
    assert pipe.calls[-1]["num_inference_steps"] == 10
    assert pipe.calls[-1]["guidance_scale"] == pytest.approx(2.0)


def test_stream_emits_progress_then_result():
    pipe = FakePipeline()
    backend = make_backend(pipe)

    items = asyncio.run(_collect(backend.stream({"prompt": "x", "steps": 5})))

    progress = [i for i in items if not i["done"]]
    assert len(progress) == 5
    assert [p["step"] for p in progress] == [1, 2, 3, 4, 5]
    assert progress[0]["total_steps"] == 5

    final = items[-1]
    assert final["done"] is True
    assert len(final["result"]["png"]) == 1
    assert final["result"]["steps"] == 5


def test_stream_emits_error_and_stops():
    pipe = FakePipeline(fail=True)
    backend = make_backend(pipe)

    items = asyncio.run(_collect(backend.stream({"prompt": "x", "steps": 3})))

    assert items[-1]["done"] is True
    assert "boom" in items[-1]["error"]
    assert "result" not in items[-1]


def test_generate_raises_on_pipeline_error():
    pipe = FakePipeline(fail=True)
    backend = make_backend(pipe)

    with pytest.raises(RuntimeError, match="boom"):
        asyncio.run(backend.generate({"prompt": "x"}))


def test_generate_before_load_raises():
    backend = SDXLBackend(pipeline_factory=lambda: FakePipeline(), device="cpu")

    with pytest.raises(RuntimeError, match="load"):
        asyncio.run(backend.generate({"prompt": "x"}))
