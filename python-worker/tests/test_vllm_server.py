import http.server
import threading

from worker.engines.vllm.server import VLLMServer


class _HealthHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b'{"status":"ok"}')
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, *args):
        pass


def test_cmd_has_tool_calling_and_limits():
    srv = VLLMServer(model="m", port=8123, max_model_len=4096,
                     gpu_memory_utilization=0.7, download_dir="/cache",
                     extra_args=["--enforce-eager"])
    cmd = " ".join(srv.cmd)
    assert "serve m" in cmd
    assert "--port 8123" in cmd
    assert "--max-model-len 4096" in cmd
    assert "--gpu-memory-utilization 0.7" in cmd
    assert "--enable-auto-tool-choice" in cmd
    assert "--tool-call-parser hermes" in cmd
    assert "--download-dir /cache" in cmd
    assert "--enforce-eager" in cmd


def test_wait_healthy_ok():
    httpd = http.server.HTTPServer(("127.0.0.1", 0), _HealthHandler)
    port = httpd.server_address[1]
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    try:
        srv = VLLMServer(model="m", port=port)
        srv.proc = None
        assert srv._wait_healthy(timeout=3) is True
    finally:
        httpd.shutdown()
        httpd.server_close()


def test_wait_healthy_timeout():
    srv = VLLMServer(model="m", port=59998)  # không có gì lắng nghe
    srv.proc = None
    assert srv._wait_healthy(timeout=1) is False
