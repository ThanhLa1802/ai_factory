"""Engine registry — chọn engine theo tên lúc khởi động worker.

Backend nặng (transformers cần torch) được import lazy, nên chọn engine
llama/vllm không phải kéo theo torch.
"""
from .base import EngineBackend

__all__ = ["EngineBackend", "TransformersBackend", "get_backend"]


def __getattr__(name):
    # PEP 562: `from worker.engines import TransformersBackend` vẫn hoạt động,
    # nhưng torch chỉ được import khi thực sự cần.
    if name == "TransformersBackend":
        from .transformers import TransformersBackend
        return TransformersBackend
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")


def get_backend(name, model_id=None, gguf=None, llama_port=8081, llama_bin="llama-server",
                gpu_layers=-1, vllm_model=None, vllm_url=None, vllm_port=8082,
                vllm_bin="vllm", vllm_max_model_len=8192,
                vllm_gpu_memory_utilization=0.9, vllm_tool_parser="hermes"):
    """Trả EngineBackend theo tên. Mỗi lúc chỉ có 1 backend được load."""
    if name == "transformers":
        from .transformers import TransformersBackend
        return TransformersBackend(model_id=model_id)
    if name == "llama":
        from .llama.backend import LlamaBackend
        return LlamaBackend(gguf=gguf, port=llama_port, bin=llama_bin, gpu_layers=gpu_layers)
    if name == "vllm":
        from .vllm.backend import VLLMBackend
        from .vllm.server import DEFAULT_MODEL
        return VLLMBackend(
            model=vllm_model or DEFAULT_MODEL, url=vllm_url, port=vllm_port,
            bin=vllm_bin, max_model_len=vllm_max_model_len,
            gpu_memory_utilization=vllm_gpu_memory_utilization,
            tool_call_parser=vllm_tool_parser,
        )
    raise ValueError(f"Unknown engine: {name!r} (expect 'transformers' | 'llama' | 'vllm')")
