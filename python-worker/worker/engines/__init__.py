"""Engine registry — chọn engine theo tên lúc khởi động worker."""
from .base import EngineBackend
from .image_base import ImageBackend
from .transformers import TransformersBackend

__all__ = ["EngineBackend", "ImageBackend", "TransformersBackend", "get_backend",
           "get_image_backend"]


def get_backend(name, model_id=None, gguf=None, llama_port=8081, llama_bin="llama-server",
                gpu_layers=-1):
    """Trả EngineBackend theo tên. Mỗi lúc chỉ có 1 backend được load."""
    if name == "transformers":
        return TransformersBackend(model_id=model_id)
    if name == "llama":
        from .llama.backend import LlamaBackend
        return LlamaBackend(gguf=gguf, port=llama_port, bin=llama_bin, gpu_layers=gpu_layers)
    if name == "vl":
        from .vision import VisionBackend
        return VisionBackend(model_id=model_id)
    raise ValueError(f"Unknown engine: {name!r} (expect 'transformers' | 'llama' | 'vl')")


def get_image_backend(name, model_id=None, variant="base", cpu_offload=False,
                      vae_tiling=True, scheduler=None):
    """Trả ImageBackend theo tên (hiện chỉ 'sdxl'). Import lazy để không kéo
    diffusers khi chỉ chạy text worker."""
    if name in ("sdxl", "diffusers"):
        from .sdxl import SDXLBackend
        return SDXLBackend(model_id=model_id, variant=variant, cpu_offload=cpu_offload,
                           vae_tiling=vae_tiling, scheduler=scheduler)
    raise ValueError(f"Unknown image engine: {name!r} (expect 'sdxl')")
