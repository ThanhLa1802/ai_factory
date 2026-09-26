"""VLLMBackend — proxy gRPC request → vLLM OpenAI-compatible server.

Hai chế độ:
  * spawn: `url` là None → khởi động subprocess `vllm serve` local (dev/docker).
  * remote: `url` được set → nối tới server đang chạy sẵn. Đây là đường Kubernetes:
    vLLM chạy như Deployment + Service riêng, pod worker chỉ trỏ tới
    `http://vllm:8000`.
"""
from ..openai_compat import OpenAICompatBackend, OpenAICompatClient
from .server import DEFAULT_MODEL, DEFAULT_TOOL_PARSER, VLLMServer


class VLLMBackend(OpenAICompatBackend):
    def __init__(self, model=DEFAULT_MODEL, url=None, port=8082, bin="vllm",
                 max_model_len=8192, gpu_memory_utilization=0.9,
                 tool_call_parser=DEFAULT_TOOL_PARSER, download_dir=None):
        super().__init__()
        # vLLM phục vụ model dưới chính path/tên truyền lúc khởi động, nên
        # `model` gửi trong body phải khớp `--model` của server (kể cả remote).
        self.model = model
        if url:
            self.server = None
            base_url = url.rstrip("/")
        else:
            self.server = VLLMServer(
                model=model, port=port, bin=bin, max_model_len=max_model_len,
                gpu_memory_utilization=gpu_memory_utilization,
                tool_call_parser=tool_call_parser, download_dir=download_dir,
            )
            base_url = self.server.base_url
        self.client = OpenAICompatClient(base_url=base_url, label="vllm")

    def load(self):
        if self.server:
            self.server.start()

    def unload(self):
        if self.server:
            self.server.stop()
