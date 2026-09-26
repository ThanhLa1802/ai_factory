"""LlamaBackend — map gRPC request ↔ OpenAI API; sinh event dict giống TransformersBackend."""
from ..openai_compat import (
    STOP_FINISH,  # noqa: F401  (re-export, tests import from here)
    OpenAICompatBackend,
    build_openai_request,  # noqa: F401
    to_openai_messages,  # noqa: F401
    usage_dict as _usage_dict,  # noqa: F401
)
from .client import LlamaClient
from .server import LlamaServer


class LlamaBackend(OpenAICompatBackend):
    def __init__(self, gguf, port=8081, bin="llama-server", gpu_layers=-1):
        super().__init__()
        self.model = "qwen3.5-9b"
        self.server = LlamaServer(gguf, port=port, bin=bin, gpu_layers=gpu_layers)
        self.client = LlamaClient(base_url=self.server.base_url)

    def load(self):
        self.server.start()

    def unload(self):
        self.server.stop()
