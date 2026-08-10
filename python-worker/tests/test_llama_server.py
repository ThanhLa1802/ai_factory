import http.server
import threading

from worker.engines.llama.server import LlamaServer


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


def test_wait_healthy_ok():
    httpd = http.server.HTTPServer(("127.0.0.1", 0), _HealthHandler)
    port = httpd.server_address[1]
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    try:
        srv = LlamaServer(gguf="dummy.gguf", port=port)
        srv.proc = None
        assert srv._wait_healthy(timeout=3) is True
    finally:
        httpd.shutdown()
        httpd.server_close()


def test_wait_healthy_timeout():
    srv = LlamaServer(gguf="dummy.gguf", port=59999)  # không có gì lắng nghe
    srv.proc = None
    assert srv._wait_healthy(timeout=1) is False
