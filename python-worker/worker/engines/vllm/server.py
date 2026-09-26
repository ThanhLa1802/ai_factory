"""VLLMServer — spawn/stop subprocess `vllm serve` và chờ `/health`.

Dùng cho dev/docker (spawn local). Trên Kubernetes, vLLM chạy như một pod riêng,
nên `VLLMBackend` nối bằng URL và KHÔNG gọi launcher này.
"""
import subprocess
import time
import urllib.error
import urllib.request

DEFAULT_MODEL = "Qwen/Qwen2.5-1.5B-Instruct"
# Qwen2.x dùng định dạng tool-call kiểu Hermes (theo tài liệu vLLM).
DEFAULT_TOOL_PARSER = "hermes"


class VLLMServer:
    def __init__(self, model=DEFAULT_MODEL, host="127.0.0.1", port=8082,
                 bin="vllm", max_model_len=8192, gpu_memory_utilization=0.9,
                 tool_call_parser=DEFAULT_TOOL_PARSER, download_dir=None,
                 extra_args=None):
        self.model = model
        self.bin = bin
        self.host = host
        self.port = port
        self.proc = None
        self._log_path = None
        self._log_file = None
        self.cmd = [
            bin, "serve", model,
            "--host", host, "--port", str(port),
            "--max-model-len", str(max_model_len),
            "--gpu-memory-utilization", str(gpu_memory_utilization),
            # Tool calling phải bật lúc khởi động, nếu không request kèm `tools`
            # sẽ bị server từ chối.
            "--enable-auto-tool-choice",
            "--tool-call-parser", tool_call_parser,
        ]
        if download_dir:
            self.cmd += ["--download-dir", download_dir]
        if extra_args:
            self.cmd += list(extra_args)

    @property
    def base_url(self):
        return f"http://{self.host}:{self.port}"

    def start(self, timeout=600.0):
        print(f"[vllm] Starting vLLM server: {' '.join(self.cmd)}")
        self._log_path = f"vllm-server-{self.port}.log"
        self._log_file = open(self._log_path, "a")
        try:
            self.proc = subprocess.Popen(self.cmd, stdout=self._log_file, stderr=subprocess.STDOUT)
        except OSError as e:
            self._log_file.close()
            raise RuntimeError(
                f"Không spawn được vLLM (bin={self.bin!r}): {e}. "
                f"Kiểm tra vLLM đã cài chưa (`pip install vllm`) hoặc --vllm-bin/PATH."
            )
        if not self._wait_healthy(timeout):
            self.stop()
            raise RuntimeError(
                f"vLLM khởi động thất bại trong {timeout}s (base_url={self.base_url}). "
                f"Xem log {self._log_path}."
            )
        print(f"[vllm] vLLM ready at {self.base_url}")

    def _wait_healthy(self, timeout):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(f"{self.base_url}/health", timeout=2) as resp:
                    if resp.status == 200:
                        return True
            except (urllib.error.URLError, OSError):
                pass
            if self.proc and self.proc.poll() is not None:
                return False  # tiến trình thoát sớm
            time.sleep(1.0)
        return False

    def stop(self):
        if self.proc and self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.proc.kill()
        self.proc = None
        if getattr(self, "_log_file", None):
            self._log_file.close()
            self._log_file = None
