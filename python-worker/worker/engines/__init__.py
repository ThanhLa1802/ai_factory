"""Engine registry — chọn engine theo tên lúc khởi động worker."""
from .base import EngineBackend
from .transformers import TransformersBackend

__all__ = ["EngineBackend", "TransformersBackend", "get_backend"]


def get_backend(name, model_id=None, gguf=None, llama_port=8081, llama_bin="llama-server"):
    """Trả EngineBackend theo tên. Mỗi lúc chỉ có 1 backend được load."""
    if name == "transformers":
        return TransformersBackend(model_id=model_id)
    if name == "llama":
        from .llama.backend import LlamaBackend
        return LlamaBackend(gguf=gguf, port=llama_port, bin=llama_bin)
    raise ValueError(f"Unknown engine: {name!r} (expect 'transformers' | 'llama')")
