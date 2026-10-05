"""Test doubles cho image backend (CPU-only, không cần diffusers/model thật).

`FakePipeline` mô phỏng interface `StableDiffusionXLPipeline` mà `SDXLBackend`
dùng: `.to()`/`enable_vae_tiling()`/`set_progress_bar_config()`, và
`__call__(**kwargs) -> .images` (list PIL). Ghi lại kwargs để kiểm tra
seed/negative/guidance/steps được truyền đúng.
"""

from types import SimpleNamespace

from PIL import Image


class FakePipeline:
    def __init__(self, fail: bool = False):
        self.fail = fail
        self.calls = []
        self.to_device = None
        self.vae_tiling = False
        self.cpu_offload = False
        self.progress_disabled = None

    def to(self, device):
        self.to_device = device
        return self

    def enable_vae_tiling(self):
        self.vae_tiling = True

    def enable_model_cpu_offload(self):
        self.cpu_offload = True

    def set_progress_bar_config(self, disable=False):
        self.progress_disabled = disable

    def __call__(self, **kwargs):
        self.calls.append(kwargs)
        if self.fail:
            raise RuntimeError("boom")

        prompt = kwargs.get("prompt", "")
        n = int(kwargs.get("num_images_per_prompt", 1))
        w = int(kwargs.get("width", 64))
        h = int(kwargs.get("height", 64))
        images = [
            Image.new("RGB", (w, h), ((i * 37 + len(prompt)) % 256, (i * 11) % 256, 128))
            for i in range(n)
        ]

        cb = kwargs.get("callback_on_step_end")
        if cb is not None:
            for step in range(int(kwargs.get("num_inference_steps", 1))):
                cb(self, step, 0, {})
        return SimpleNamespace(images=images)
