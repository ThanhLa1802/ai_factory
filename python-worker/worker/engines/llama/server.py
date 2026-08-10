"""LlamaServer — spawn/stop subprocess llama-server (llama.cpp), chờ /health."""
import subprocess
import time
import urllib.error
import urllib.request


class LlamaServer:
    def __init__(self, gguf, host="127.0.0.1", port=8081, bin="llama-server",
                 ctx_size=8192, threads=8, gpu_layers=-1):
        self.gguf = gguf
        self.host = host
        self.port = port
        self.bin = bin
        self.proc = None
        self._log_path = None
        self._log_file = None
        self.cmd = [
            bin, "-m", gguf,
            "--host", host, "--port", str(port),
            "--n-gpu-layers", str(gpu_layers),
            "--ctx-size", str(ctx_size),
            "--threads", str(threads),
        ]

    @property
    def base_url(self):
        return f"http://{self.host}:{self.port}"

    def start(self, timeout=120.0):
        print(f"[llama] Starting llama-server: {' '.join(self.cmd)}")
        self._log_path = f"llama-server-{self.port}.log"
        self._log_file = open(self._log_path, "a")
        try:
            self.proc = subprocess.Popen(self.cmd, stdout=self._log_file, stderr=subprocess.STDOUT)
        except OSError as e:
            self._log_file.close()
            raise RuntimeError(
                f"Không spawn được llama-server (bin={self.bin!r}): {e}. "
                f"Kiểm tra --llama-bin/PATH và file GGUF --gguf ({self.gguf})."
            )
        if not self._wait_healthy(timeout):
            self.stop()
            raise RuntimeError(
                f"llama-server khởi động thất bại trong {timeout}s (base_url={self.base_url}). "
                f"Xem log {self._log_path}."
            )
        print(f"[llama] llama-server ready at {self.base_url}")

    def _wait_healthy(self, timeout):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(f"{self.base_url}/health", timeout=1) as resp:
                    if resp.status == 200:
                        return True
            except (urllib.error.URLError, OSError):
                pass
            if self.proc and self.proc.poll() is not None:
                return False  # tiến trình thoát sớm
            time.sleep(0.5)
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
