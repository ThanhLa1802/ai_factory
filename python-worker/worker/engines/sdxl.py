"""SDXLBackend — text-to-image với SDXL 1.0 base (diffusers).

Model mặc định: `stabilityai/stable-diffusion-xl-base-1.0`, fp16, native 1024px.
Chạy trên GPU 12GB (RTX 3060): ~9.6GB đỉnh; batch=1, VAE tiling bật mặc định.

Chạy trong **process riêng** (``python -m worker.image_server --engine sdxl``)
vì text model và image model không cùng nằm trong 12GB (spec D3).

`pipeline_factory` cho phép test CPU-only bằng một pipeline giả (không tải model).
"""

import asyncio
import io
import random

import torch
from PIL import Image

from .image_base import ImageBackend

DEFAULT_MODEL_ID = "stabilityai/stable-diffusion-xl-base-1.0"
TURBO_MODEL_ID = "stabilityai/sdxl-turbo"

# Variant quyết định model + tham số mặc định. Turbo hội tụ sau 1–4 step,
# guidance_scale=0 (đúng đặc tả sdxl-turbo).
_VARIANTS = {
    "base": {"model_id": DEFAULT_MODEL_ID, "steps": 25, "guidance_scale": 7.0, "scheduler": None},
    "turbo": {"model_id": TURBO_MODEL_ID, "steps": 4, "guidance_scale": 0.0, "scheduler": None},
}

_SCHEDULERS = {
    "euler": "EulerDiscreteScheduler",
    "dpmpp_2m": "DPMSolverMultistepScheduler",
}


class SDXLBackend(ImageBackend):
    def __init__(self, model_id=None, variant="base", device=None, dtype=None,
                 cpu_offload=False, vae_tiling=True, scheduler=None,
                 pipeline_factory=None):
        super().__init__()
        spec = _VARIANTS.get(variant, _VARIANTS["base"])
        self.variant = variant if variant in _VARIANTS else "base"
        self.model_id = model_id or spec["model_id"]
        self.default_steps = spec["steps"]
        self.default_guidance = spec["guidance_scale"]
        self.device = torch.device(device) if device is not None else torch.device(
            "cuda" if torch.cuda.is_available() else "cpu"
        )
        if dtype is not None:
            self.dtype = dtype
        else:
            # fp16 chỉ có lợi trên GPU; CPU cần fp32 cho ổn định.
            self.dtype = torch.float16 if self.device.type == "cuda" else torch.float32
        self.cpu_offload = cpu_offload
        self.vae_tiling = vae_tiling
        self.scheduler_name = scheduler or spec.get("scheduler")
        self._pipeline_factory = pipeline_factory
        self.pipe = None

    # -- lifecycle ---------------------------------------------------------

    def load(self) -> None:
        if self.pipe is not None:
            return
        if self._pipeline_factory is not None:
            pipe = self._pipeline_factory()
        else:
            pipe = self._load_real_pipeline()

        if self.cpu_offload and hasattr(pipe, "enable_model_cpu_offload"):
            # Offload từng module GPU<->CPU; dùng khi VRAM chật (D7).
            pipe.enable_model_cpu_offload()
        elif hasattr(pipe, "to"):
            pipe = pipe.to(self.device)
        if self.vae_tiling and hasattr(pipe, "enable_vae_tiling"):
            pipe.enable_vae_tiling()
        if hasattr(pipe, "set_progress_bar_config"):
            pipe.set_progress_bar_config(disable=True)
        self.pipe = pipe

    def _load_real_pipeline(self):
        from diffusers import StableDiffusionXLPipeline

        kwargs = {"torch_dtype": self.dtype, "use_safetensors": True}
        if self.dtype == torch.float16:
            kwargs["variant"] = "fp16"
        pipe = StableDiffusionXLPipeline.from_pretrained(self.model_id, **kwargs)
        if self.scheduler_name:
            self._apply_scheduler(pipe)
        return pipe

    def _apply_scheduler(self, pipe) -> None:
        name = _SCHEDULERS.get(self.scheduler_name.lower().replace("-", "_"))
        if name is None or not hasattr(pipe, "scheduler"):
            return
        import diffusers

        cls = getattr(diffusers, name, None)
        if cls is not None:
            pipe.scheduler = cls.from_config(pipe.scheduler.config)

    def unload(self) -> None:
        pipe, self.pipe = self.pipe, None
        if pipe is not None:
            del pipe
        if torch.cuda.is_available():
            torch.cuda.empty_cache()

    # -- generation --------------------------------------------------------

    async def generate(self, req: dict) -> dict:
        async with self._gen_lock:
            return await asyncio.to_thread(self._run, req, None)

    async def stream(self, req: dict):
        async with self._gen_lock:
            loop = asyncio.get_running_loop()
            queue: asyncio.Queue = asyncio.Queue()

            def emit(item):
                loop.call_soon_threadsafe(queue.put_nowait, item)

            def on_progress(step, total):
                emit({"step": step, "total_steps": total, "done": False})

            def run():
                try:
                    result = self._run(req, on_progress)
                    emit({
                        "step": result["steps"],
                        "total_steps": result["steps"],
                        "done": True,
                        "result": result,
                    })
                except Exception as e:  # noqa: BLE001 — surface mọi lỗi qua stream
                    emit({"done": True, "error": str(e)})

            fut = loop.run_in_executor(None, run)
            while True:
                item = await queue.get()
                yield item
                if item.get("done"):
                    break
            await fut

    def _run(self, req: dict, progress_cb) -> dict:
        if self.pipe is None:
            raise RuntimeError("SDXLBackend.load() chưa được gọi")

        width = int(req.get("width") or 1024)
        height = int(req.get("height") or 1024)
        num_images = max(1, int(req.get("num_images") or 1))
        steps = int(req.get("steps") or self.default_steps)
        guidance = req.get("guidance_scale")
        guidance = self.default_guidance if guidance is None else float(guidance)
        if steps <= 0:
            steps = self.default_steps

        seed = req.get("seed", -1)
        if seed is None or int(seed) < 0:
            seed = random.randint(0, 2**31 - 1)
        seed = int(seed)
        generator = torch.Generator(device="cpu").manual_seed(seed)

        callback = None
        if progress_cb is not None:
            def callback(pipe, step, timestep, callback_kwargs):  # noqa: ARG001
                progress_cb(int(step) + 1, steps)
                return callback_kwargs

        with torch.inference_mode():
            result = self.pipe(
                prompt=req.get("prompt", ""),
                negative_prompt=req.get("negative_prompt") or None,
                width=width,
                height=height,
                num_inference_steps=steps,
                guidance_scale=guidance,
                generator=generator,
                num_images_per_prompt=num_images,
                callback_on_step_end=callback,
                output_type="pil",
            )

        pngs = [self._encode_png(img) for img in result.images]
        return {
            "png": pngs,
            "width": width,
            "height": height,
            "seed": seed,
            "steps": steps,
            "scheduler": req.get("scheduler") or self.scheduler_name or "default",
        }

    @staticmethod
    def _encode_png(image) -> bytes:
        if isinstance(image, (bytes, bytearray)):
            return bytes(image)
        buf = io.BytesIO()
        image.save(buf, format="PNG")
        return buf.getvalue()
