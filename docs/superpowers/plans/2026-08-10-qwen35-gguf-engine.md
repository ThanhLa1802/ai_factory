# Qwen3.5-9B GGUF Engine (llama.cpp) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Thêm engine thứ hai — **Qwen3.5-9B GGUF** qua **llama-server** — vào python-worker, song song với core transformers (Qwen2.5-Coder-7B); Go server + proto không đổi.

**Architecture:** Giới thiệu `EngineBackend` interface trong Python worker; hai implementation: `TransformersBackend` (wrap `InferenceEngine`+`BatchEngine` hiện có) và `LlamaBackend` (spawn `llama-server` subprocess, proxy gRPC → OpenAI-compatible HTTP streaming). Backend chọn lúc khởi động worker qua flag `--engine`. Sơ đồ & thiết kế đầy đủ: `docs/superpowers/specs/2026-08-10-qwen35-gguf-engine-design.md`.

**Tech Stack:** Python 3.11+, `httpx>=0.27` (async HTTP streaming), `huggingface_hub` (tải GGUF), `grpc.aio`, `llama-server` (llama.cpp prebuilt Windows), GGUF Qwen3.5-9B Q4_K_M.

## Global Constraints

- **KHÔNG** đổi `proto/inference.proto`, Go server, agentic loop, session manager.
- `--engine transformers` là **default** (Qwen2.5-Coder-7B); Qwen3.5-9B chỉ khi `--engine llama`.
- Backend chọn **lúc khởi động** worker — mỗi lúc chỉ load 1 model (12GB VRAM).
- `llama-server`: host `127.0.0.1`, port **8081**, `--n-gpu-layers -1`, `--ctx-size 8192`, `--threads 8`.
- GGUF: Qwen3.5-9B **Q4_K_M** (~6GB) vào `models/`.
- Event dict shape (mọi backend phát ra): `{"type":"token","token":str}` | `{"type":"tool_use","id","name","arguments"}` | `{"type":"final","stop_reason","finish_reason","usage","token"?}`.
- `generate_batch(requests)` trả async iterator `(request_id, event)`; `requests` là list dict có key `request_id`, `messages`, `system_prompt`, `sampling_params`, `tools`.
- Tools: format OpenAI `{"type":"function","function":{name,description,parameters}}` — tái dùng `_tools_from_proto` trong `server.py`.
- Python ≥3.11, Windows native. Dep mới trong `pyproject.toml`: `httpx>=0.27`, `huggingface_hub`.

---

### Task 1: Baseline — venv, deps, git, test xanh

**Files:**
- Modify: `python-worker/pyproject.toml`
- Test: `python-worker/tests/test_tokenizer.py` (đã có)

**Interfaces:**
- Consumes: —
- Produces: môi trường chạy `pytest`; repo git.

- [ ] **Step 1: Thêm deps mới vào `pyproject.toml`**

Trong block `dependencies` thêm:
```toml
    "httpx>=0.27",
    "huggingface_hub",
```
Block `[project.optional-dependencies] dev` giữ nguyên: `pytest`, `pytest-asyncio`.

- [ ] **Step 2: Tạo venv + cài deps (Windows)**

```powershell
cd python-worker
python -m venv .venv
.\.venv\Scripts\python -m pip install --upgrade pip
.\.venv\Scripts\python -m pip install -e ".[dev]"
```
⚠️ `torch`/`bitsandbytes` là download lớn (~2.5GB+). Nếu máy đã có env khác có đủ deps, có thể dùng env đó thay vì venv — miễn `pytest` chạy được `tests/test_tokenizer.py`.

- [ ] **Step 3: Tạo `.gitignore`** (root — trước khi git init, tránh stage `.venv`/`models`/cache)

Tạo file `G:\STUDY\AI\ai_factory\.gitignore`:

```gitignore
# Python
.venv/
__pycache__/
*.pyc
.pytest_cache/

# Model files (GGUF lớn ~6GB, không commit)
models/

# Env / secrets
.env

# Tooling
.superpowers/
```

- [ ] **Step 4: Khởi tạo git** (working copy hiện chưa là repo)

```powershell
cd G:\STUDY\AI\ai_factory
git init -b main
git add -A
git commit -m "chore: baseline ai_factory (Qwen2.5-Coder-7B, worker, proto, docs)"
```
> Nếu user không muốn git: bỏ qua bước này và bỏ qua các bước "Commit" ở các task sau.

- [ ] **Step 5: Chạy test hiện có để xác nhận baseline xanh**

Run: `cd python-worker; .\.venv\Scripts\python -m pytest tests/ -q`
Expected: PASS (test tokenizer đối chiếu IDs == HF).

- [ ] **Step 6: Commit**

```bash
git add python-worker/pyproject.toml .gitignore
git commit -m "chore: add httpx, huggingface_hub deps"
```

---

### Task 2: EngineBackend interface + registry + TransformersBackend

**Files:**
- Create: `python-worker/worker/engines/__init__.py`
- Create: `python-worker/worker/engines/base.py`
- Create: `python-worker/worker/engines/transformers.py`
- Test: `python-worker/tests/test_engines.py`

**Interfaces:**
- Consumes: `worker.engine.get_engine`, `worker.batch_engine.BatchEngine` (đã có).
- Produces:
  - `EngineBackend` (base, abstract): `load()`, `unload()`, `async generate(messages, sampling_params, tools=None, cancel_event=None)`, `generate_batch(requests)`.
  - `TransformersBackend(model_id=None)`.
  - `get_backend(name, model_id=None, gguf=None, llama_port=8081, llama_bin="llama-server")`.

- [ ] **Step 1: Viết test fail** — `tests/test_engines.py`

```python
import pytest

from worker.engines import get_backend, TransformersBackend


def test_registry_unknown_raises():
    with pytest.raises(ValueError):
        get_backend("nope")


@pytest.mark.skip(reason="LlamaBackend chưa tồn tại cho tới Task 6 — bỏ skip ở Task 6")
def test_registry_llama():
    b = get_backend("llama", gguf="dummy.gguf", llama_port=8123)
    assert type(b).__name__ == "LlamaBackend"


@pytest.mark.asyncio
async def test_transformers_generate_delegates(monkeypatch):
    events = [
        {"type": "token", "token": "hi"},
        {"type": "final", "stop_reason": "STOP_END_TURN",
         "finish_reason": "stop", "usage": {}},
    ]

    class StubEngine:
        async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
            for ev in events:
                yield ev

    monkeypatch.setattr("worker.engines.transformers.get_engine",
                        lambda *a, **k: StubEngine())
    backend = TransformersBackend()
    got = [ev async for ev in backend.generate([], {})]
    assert got == events
```

- [ ] **Step 2: Chạy test để thấy fail**

Run: `cd python-worker; .\.venv\Scripts\python -m pytest tests/test_engines.py -v`
Expected: FAIL — `ModuleNotFoundError: No module named 'worker.engines'`.

- [ ] **Step 3: Tạo `worker/engines/base.py`**

```python
"""EngineBackend — interface tối thiểu cho engine inference (swap engine sau interface)."""
from abc import ABC, abstractmethod


class EngineBackend(ABC):
    """Event dict shape (generate / generate_batch):
      {"type": "token", "token": str}
      {"type": "tool_use", "id": str, "name": str, "arguments": str}
      {"type": "final", "stop_reason": str, "finish_reason": str, "usage": dict, "token": str|None}
    """

    @abstractmethod
    def load(self) -> None:
        """Load model / spawn engine subprocess."""

    @abstractmethod
    def unload(self) -> None:
        """Giải phóng model / dừng subprocess."""

    @abstractmethod
    async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
        """Stream events cho 1 request. messages đã gồm system prompt (nếu có)."""

    @abstractmethod
    def generate_batch(self, requests):
        """Trả async iterator (request_id, event). requests: list[dict] (system_prompt là key riêng)."""
```

- [ ] **Step 4: Tạo `worker/engines/transformers.py`**

```python
"""TransformersBackend — wrap InferenceEngine + BatchEngine hiện có (Qwen2.5-Coder-7B)."""
from ..engine import get_engine
from ..batch_engine import BatchEngine
from .base import EngineBackend


class TransformersBackend(EngineBackend):
    def __init__(self, model_id=None):
        self.engine = get_engine(model_id) if model_id else get_engine()
        self._batch = None

    def load(self):
        self.engine.load()

    def unload(self):
        self.engine.unload()

    async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
        async for event in self.engine.generate(
            messages=messages,
            sampling_params=sampling_params,
            tools=tools,
            cancel_event=cancel_event,
        ):
            yield event

    def generate_batch(self, requests):
        return self._get_batch().generate_batch(requests)

    def _get_batch(self):
        if self._batch is None:
            self._batch = BatchEngine(
                self.engine.model,
                self.engine.tokenizer,
                self.engine.hf_tokenizer,
            )
        return self._batch
```

- [ ] **Step 5: Tạo `worker/engines/__init__.py`** (registry)

```python
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
```

> `LlamaBackend` import trễ (lazy) để module chưa tồn tại ở Task 2 vẫn không phá `test_registry_unknown_raises`/`test_registry_llama` — nhưng test này sẽ FAIL cho tới Task 6 tạo `llama/backend.py`. Xử lý trong Task 6.

- [ ] **Step 6: Chạy test để thấy pass**

Run: `cd python-worker; .\.venv\Scripts\python -m pytest tests/test_engines.py -v`
Expected: PASS — `test_registry_unknown_raises`, `test_transformers_generate_delegates`; `test_registry_llama` bị **skip** (bỏ skip ở Task 6).

- [ ] **Step 7: Commit**

```bash
git add python-worker/worker/engines/ python-worker/tests/test_engines.py
git commit -m "feat(worker): EngineBackend interface + registry + TransformersBackend"
```

---

### Task 3: Nối server.py sang EngineBackend (giữ nguyên hành vi transformers)

**Files:**
- Modify: `python-worker/worker/server.py` (servicers, `serve`, `main`)
- Test: `python-worker/tests/test_server_backend.py`

**Interfaces:**
- Consumes: `EngineBackend`/`get_backend` (Task 2).
- Produces: `serve(port=50051, model_id=None, engine_name="transformers", gguf=None, llama_port=8081, llama_bin="llama-server")`; servicers nhận `backend` thay vì `engine`.

- [ ] **Step 1: Viết test fail** — `tests/test_server_backend.py`

```python
import pytest

from worker.server import InferenceServicer, _build_response


@pytest.mark.asyncio
async def test_inference_servicer_streams_backend_events():
    class FakeBackend:
        async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
            yield {"type": "token", "token": "Hi"}
            yield {"type": "final", "stop_reason": "STOP_END_TURN",
                   "finish_reason": "stop", "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}

    class FakeContext:
        def __init__(self):
            self.written = []

        def cancelled(self):
            return False

        async def write(self, resp):
            self.written.append(resp)

    req = type("Req", (), {
        "request_id": "r1", "session_id": "s1",
        "messages": [], "tools": [], "system_prompt": "",
        "sampling_params": type("SP", (), {
            "max_tokens": 0, "temperature": 0, "top_p": 0, "top_k": 0, "stop_sequences": [],
        })(),
    })()

    ctx = FakeContext()
    svc = InferenceServicer(FakeBackend())
    await svc.Generate(req, ctx)
    assert [r.event_type for r in ctx.written] == [1, 3]  # EVENT_TOKEN=1, EVENT_FINAL=3
    assert ctx.written[-1].stop_reason == 1  # STOP_END_TURN


def test_build_response_token():
    resp = _build_response({"type": "token", "token": "x"})
    assert resp.event_type == 1 and resp.token == "x"
```

> `req` dùng `type("Req", ...)` giả lập proto message — gọi đủ các field servicer đọc. Nếu `request.tools` là `[]` (falsy) → `tools=None`.

- [ ] **Step 2: Chạy test để thấy fail**

Run: `cd python-worker; .\.venv\Scripts\python -m pytest tests/test_server_backend.py -v`
Expected: FAIL — `TypeError: __init__()` nhận `engine` nhưng test truyền `FakeBackend` (servicer hiện gọi `self.engine.generate`).

- [ ] **Step 3: Sửa `InferenceServicer` (server.py)**

Đổi dòng import `from .engine import get_engine, InferenceEngine` → `from .engine import get_engine`. Trong `InferenceServicer.__init__` đổi tham số `engine` → `backend` và `self.engine = engine` → `self.backend = backend`. Trong `Generate`, đổi dòng gọi:
```python
            async for event in self.backend.generate(
                messages=engine_messages,
                sampling_params=sampling_params,
                tools=tools,
                cancel_event=cancel_event,
            ):
```

- [ ] **Step 4: Sửa `BatchInferenceServicer` (server.py)**

- `__init__(self, backend)`; bỏ thuộc tính `self.engine`/`self.batch_engine` và phương thức `_get_batch_engine`.
- Trong `BatchGenerate`, thay block `batch_engine = self._get_batch_engine()` + `batch_engine.generate_batch(...)` bằng:
```python
            async for req_id, event in self.backend.generate_batch(batch_requests):
```

- [ ] **Step 5: Sửa `serve()` và `main()` (server.py)**

`serve` signature:
```python
async def serve(port: int = DEFAULT_PORT, model_id: str | None = None,
                engine_name: str = "transformers", gguf: str | None = None,
                llama_port: int = 8081, llama_bin: str = "llama-server"):
```
Body thay block load model:
```python
    from .engines import get_backend
    backend = get_backend(engine_name, model_id=model_id, gguf=gguf,
                          llama_port=llama_port, llama_bin=llama_bin)
    backend.load()
```
Đổi 2 chỗ `InferenceServicer(engine)` → `InferenceServicer(backend)`; `BatchInferenceServicer(engine)` → `BatchInferenceServicer(backend)`. Cuối shutdown: `engine.unload()` → `backend.unload()`.

`main()` thêm argparse:
```python
    parser.add_argument("--engine", type=str, default="transformers",
                        help="transformers | llama")
    parser.add_argument("--gguf", type=str, default=None,
                        help="Path hoặc repo GGUF (chỉ khi --engine llama)")
    parser.add_argument("--llama-port", type=int, default=8081)
    parser.add_argument("--llama-bin", type=str, default="llama-server")
```
và `asyncio.run(serve(port=args.port, model_id=args.model, engine_name=args.engine,
    gguf=args.gguf, llama_port=args.llama_port, llama_bin=args.llama_bin))`.

- [ ] **Step 6: Chạy test để thấy pass**

Run: `cd python-worker; .\.venv\Scripts\python -m pytest tests/test_server_backend.py tests/test_engines.py -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add python-worker/worker/server.py python-worker/tests/test_server_backend.py
git commit -m "refactor(worker): servicers dùng EngineBackend; thêm --engine/--gguf/--llama-* flags"
```

---

### Task 4: LlamaServer — quản lý subprocess llama-server

**Files:**
- Create: `python-worker/worker/engines/llama/__init__.py`
- Create: `python-worker/worker/engines/llama/server.py`
- Test: `python-worker/tests/test_llama_server.py`

**Interfaces:**
- Consumes: —
- Produces: `LlamaServer(gguf, host="127.0.0.1", port=8081, bin="llama-server", ctx_size=8192, threads=8, gpu_layers=-1)` với `.start(timeout=120)`, `.stop()`, `.base_url`, `._wait_healthy(timeout)`.

- [ ] **Step 1: Viết test fail** — `tests/test_llama_server.py`

```python
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
```

- [ ] **Step 2: Chạy test để thấy fail**

Run: `cd python-worker; .\.venv\Scripts\python -m pytest tests/test_llama_server.py -v`
Expected: FAIL — `ModuleNotFoundError: worker.engines.llama`.

- [ ] **Step 3: Tạo `worker/engines/llama/__init__.py`**

```python
"""Engine llama (Qwen3.5 qua llama.cpp)."""
```

- [ ] **Step 4: Tạo `worker/engines/llama/server.py`**

```python
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
        self.proc = None
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
        self.proc = subprocess.Popen(self.cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if not self._wait_healthy(timeout):
            self.stop()
            raise RuntimeError(
                f"llama-server khởi động thất bại trong {timeout}s (base_url={self.base_url}). "
                f"Kiểm tra --gguf và --llama-bin."
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
```

- [ ] **Step 5: Chạy test để thấy pass**

Run: `cd python-worker; .\.venv\Scripts\python -m pytest tests/test_llama_server.py -v`
Expected: PASS (cả 2 test, dùng stub HTTP server — không cần llama-server thật).

- [ ] **Step 6: Commit**

```bash
git add python-worker/worker/engines/llama/ python-worker/tests/test_llama_server.py
git commit -m "feat(worker): LlamaServer subprocess manager + health check"
```

---

### Task 5: LlamaClient — transport HTTP OpenAI-compatible (SSE)

**Files:**
- Create: `python-worker/worker/engines/llama/client.py`
- Test: `python-worker/tests/test_llama_client.py`

**Interfaces:**
- Consumes: `httpx>=0.27` (Task 1).
- Produces: `LlamaClient(base_url="http://127.0.0.1:8081", transport=None)` với `async chat_completions(body) -> AsyncIterator[dict]` — yield từng JSON object mỗi dòng `data: {...}`, bỏ `[DONE]`, raise `RuntimeError` nếu HTTP ≠ 200.

- [ ] **Step 1: Viết test fail** — `tests/test_llama_client.py`

```python
import httpx
import pytest

from worker.engines.llama.client import LlamaClient

SSE = (
    'data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}\n\n'
    'data: {"choices":[{"delta":{"content":" world"},"finish_reason":null}]}\n\n'
    'data: {"choices":[{"delta":{},"finish_reason":"stop"}],'
    '"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}\n\n'
    'data: [DONE]\n\n'
)


@pytest.mark.asyncio
async def test_parses_sse_chunks():
    def handler(request):
        return httpx.Response(200, content=SSE,
                              headers={"content-type": "text/event-stream"})

    client = LlamaClient(base_url="http://test",
                         transport=httpx.MockTransport(handler))
    chunks = [c async for c in client.chat_completions({})]
    assert len(chunks) == 3
    assert chunks[0]["choices"][0]["delta"]["content"] == "Hello"
    assert chunks[2]["choices"][0]["finish_reason"] == "stop"
    assert chunks[2]["usage"]["prompt_tokens"] == 5


@pytest.mark.asyncio
async def test_http_error_raises_runtime_error():
    def handler(request):
        return httpx.Response(500, content=b"boom")

    client = LlamaClient(base_url="http://test",
                         transport=httpx.MockTransport(handler))
    with pytest.raises(RuntimeError, match="500"):
        async for _ in client.chat_completions({}):
            pass
```

- [ ] **Step 2: Chạy test để thấy fail**

Run: `cd python-worker; .\.venv\Scripts\python -m pytest tests/test_llama_client.py -v`
Expected: FAIL — `ModuleNotFoundError: worker.engines.llama.client`.

- [ ] **Step 3: Tạo `worker/engines/llama/client.py`**

```python
"""LlamaClient — transport HTTP tới llama-server (/v1/chat/completions, OpenAI-compatible).

Thin transport: yield raw JSON object của từng dòng `data: {...}`; KHÔNG hiểu event semantics
(cái đó thuộc LlamaBackend). Chấp nhận `transport` để inject httpx.MockTransport trong test.
"""
import json

import httpx


class LlamaClient:
    def __init__(self, base_url="http://127.0.0.1:8081", transport=None):
        self.base_url = base_url
        self._transport = transport

    def _client(self):
        return httpx.AsyncClient(
            base_url=self.base_url,
            timeout=httpx.Timeout(300.0, connect=10.0),
            transport=self._transport,
        )

    async def chat_completions(self, body):
        async with self._client() as client:
            async with client.stream("POST", "/v1/chat/completions", json=body) as resp:
                if resp.status_code != 200:
                    text = await resp.aread()
                    raise RuntimeError(
                        f"llama-server HTTP {resp.status_code}: "
                        f"{text.decode(errors='replace')[:500]}"
                    )
                async for line in resp.aiter_lines():
                    line = line.strip()
                    if not line.startswith("data:"):
                        continue
                    payload = line[len("data:"):].strip()
                    if not payload or payload == "[DONE]":
                        continue
                    yield json.loads(payload)
```

- [ ] **Step 4: Chạy test để thấy pass**

Run: `cd python-worker; .\.venv\Scripts\python -m pytest tests/test_llama_client.py -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add python-worker/worker/engines/llama/client.py python-worker/tests/test_llama_client.py
git commit -m "feat(worker): LlamaClient httpx SSE transport"
```

---

### Task 6: LlamaBackend — map request, parse events, batch

**Files:**
- Create: `python-worker/worker/engines/llama/backend.py`
- Test: `python-worker/tests/test_llama_backend.py`

**Interfaces:**
- Consumes: `LlamaServer` (Task 4), `LlamaClient` (Task 5), `EngineBackend` (Task 2).
- Produces:
  - `to_openai_messages(messages) -> list[dict]` — internal dicts → OpenAI Chat Completions format.
  - `build_openai_request(messages, sampling_params, tools=None) -> dict`.
  - `LlamaBackend(gguf, port=8081, bin="llama-server")` với `generate`/`generate_batch` phát event dict như spec.

- [ ] **Step 1: Viết test fail** — `tests/test_llama_backend.py`

```python
import pytest

from worker.engines.llama.backend import (
    LlamaBackend, build_openai_request, to_openai_messages,
)


class _FakeClient:
    def __init__(self, chunks):
        self._chunks = chunks

    async def chat_completions(self, body):
        for c in self._chunks:
            yield c


def test_to_openai_messages():
    msgs = [
        {"role": "user", "content": "hi"},
        {"role": "assistant", "content": "", "tool_calls": [
            {"id": "c1", "name": "read_file", "arguments": '{"path":"a.txt"}'}]},
        {"role": "tool", "tool_call_id": "c1", "content": "OK"},
    ]
    out = to_openai_messages(msgs)
    assert out[1]["tool_calls"][0]["function"]["name"] == "read_file"
    assert out[2]["role"] == "tool" and out[2]["tool_call_id"] == "c1"


def test_build_openai_request():
    body = build_openai_request(
        [{"role": "user", "content": "hi"}],
        {"max_tokens": 10, "stop_sequences": ["<|im_end|>"]},
        tools=[{"type": "function", "function": {"name": "f"}}],
    )
    assert body["tools"] and body["tool_choice"] == "auto"
    assert body["stop"] == ["<|im_end|>"]
    assert body["stream"] is True


@pytest.mark.asyncio
async def test_generate_streams_tokens_and_final():
    chunks = [
        {"choices": [{"delta": {"content": "Hel"}, "finish_reason": None}]},
        {"choices": [{"delta": {"content": "lo"}, "finish_reason": None}]},
        {"choices": [{"delta": {}, "finish_reason": "stop"}],
         "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}},
    ]
    backend = LlamaBackend.__new__(LlamaBackend)
    backend.client = _FakeClient(chunks)
    events = [ev async for ev in backend.generate([], {})]
    assert events[0] == {"type": "token", "token": "Hel"}
    assert events[1] == {"type": "token", "token": "lo"}
    assert events[2]["type"] == "final"
    assert events[2]["stop_reason"] == "STOP_END_TURN"


@pytest.mark.asyncio
async def test_generate_tool_calls_accumulated():
    chunks = [
        {"choices": [{"delta": {"tool_calls": [
            {"index": 0, "id": "call_1", "type": "function",
             "function": {"name": "read_file", "arguments": '{"pat'}}]},
            "finish_reason": None}]},
        {"choices": [{"delta": {"tool_calls": [
            {"index": 0, "function": {"arguments": 'h":"a.txt"}'}}]},
            "finish_reason": None}]},
        {"choices": [{"delta": {}, "finish_reason": "tool_calls"}]},
    ]
    backend = LlamaBackend.__new__(LlamaBackend)
    backend.client = _FakeClient(chunks)
    events = [ev async for ev in backend.generate([], {})]
    tool = events[0]
    assert tool["type"] == "tool_use" and tool["name"] == "read_file"
    assert tool["arguments"] == '{"path":"a.txt"}'
    assert events[1]["stop_reason"] == "STOP_TOOL_USE"


@pytest.mark.asyncio
async def test_generate_batch_interleaves_and_completes():
    class B(LlamaBackend):
        def __init__(self):
            self.server = None
            self.client = None

        async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
            yield {"type": "token", "token": "x"}
            yield {"type": "final", "stop_reason": "STOP_END_TURN",
                   "finish_reason": "stop", "usage": {}}

    backend = B()
    reqs = [
        {"request_id": "a", "messages": [{"role": "user", "content": "1"}],
         "system_prompt": "", "sampling_params": {}, "tools": None},
        {"request_id": "b", "messages": [{"role": "user", "content": "2"}],
         "system_prompt": "", "sampling_params": {}, "tools": None},
    ]
    pairs = [(rid, ev["type"]) async for rid, ev in backend.generate_batch(reqs)]
    assert len(pairs) == 4
    assert {rid for rid, t in pairs if t == "final"} == {"a", "b"}
```

- [ ] **Step 2: Chạy test để thấy fail**

Run: `cd python-worker; .\.venv\Scripts\python -m pytest tests/test_llama_backend.py -v`
Expected: FAIL — `ModuleNotFoundError: worker.engines.llama.backend`.

- [ ] **Step 3: Tạo `worker/engines/llama/backend.py`**

```python
"""LlamaBackend — map gRPC request ↔ OpenAI API; sinh event dict giống TransformersBackend."""
import asyncio

from ..base import EngineBackend
from .server import LlamaServer
from .client import LlamaClient

STOP_FINISH = {
    "stop": "STOP_END_TURN",
    "length": "STOP_MAX_TOKENS",
    "tool_calls": "STOP_TOOL_USE",
}


def to_openai_messages(messages):
    """Internal message dicts (từ _messages_from_proto) → OpenAI Chat Completions format."""
    out = []
    for m in messages:
        role = m.get("role", "user")
        if role == "tool":
            out.append({
                "role": "tool",
                "tool_call_id": m.get("tool_call_id", ""),
                "content": m.get("content", ""),
            })
            continue
        fm = {"role": role, "content": m.get("content", "")}
        if m.get("tool_calls"):
            fm["tool_calls"] = [
                {"id": tc["id"], "type": "function",
                 "function": {"name": tc["name"], "arguments": tc.get("arguments", "{}")}}
                for tc in m["tool_calls"]
            ]
        out.append(fm)
    return out


def build_openai_request(messages, sampling_params, tools=None):
    body = {
        "model": "qwen3.5-9b",
        "messages": to_openai_messages(messages),
        "max_tokens": sampling_params.get("max_tokens", 1024),
        "temperature": sampling_params.get("temperature", 0.7),
        "top_p": sampling_params.get("top_p", 0.9),
        "top_k": sampling_params.get("top_k", 50),
        "stream": True,
    }
    if sampling_params.get("stop_sequences"):
        body["stop"] = list(sampling_params["stop_sequences"])
    if tools:
        body["tools"] = tools
        body["tool_choice"] = "auto"
    return body


class LlamaBackend(EngineBackend):
    def __init__(self, gguf, port=8081, bin="llama-server"):
        self.server = LlamaServer(gguf, port=port, bin=bin)
        self.client = LlamaClient(base_url=self.server.base_url)

    def load(self):
        self.server.start()

    def unload(self):
        self.server.stop()

    async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
        body = build_openai_request(messages, sampling_params, tools)
        tool_acc = {}
        usage = {}
        async for chunk in self.client.chat_completions(body):
            if cancel_event and cancel_event.is_set():
                return
            usage = chunk.get("usage") or usage
            choices = chunk.get("choices") or []
            if not choices:
                continue
            delta = choices[0].get("delta") or {}
            if delta.get("content"):
                yield {"type": "token", "token": delta["content"]}
            for tc in delta.get("tool_calls") or []:
                idx = tc.get("index", 0)
                slot = tool_acc.setdefault(idx, {"id": "", "name": "", "arguments": ""})
                if tc.get("id"):
                    slot["id"] = tc["id"]
                fn = tc.get("function") or {}
                if fn.get("name"):
                    slot["name"] = fn["name"]
                if fn.get("arguments"):
                    slot["arguments"] += fn["arguments"]
            finish = choices[0].get("finish_reason")
            if finish:
                stop_reason = STOP_FINISH.get(finish, "STOP_END_TURN")
                if stop_reason == "STOP_TOOL_USE":
                    for idx in sorted(tool_acc):
                        s = tool_acc[idx]
                        yield {"type": "tool_use", "id": s["id"],
                               "name": s["name"], "arguments": s["arguments"]}
                yield {"type": "final", "stop_reason": stop_reason,
                       "finish_reason": finish, "usage": _usage_dict(usage)}
                return
        yield {"type": "final", "stop_reason": "STOP_END_TURN",
               "finish_reason": "stop", "usage": _usage_dict(usage)}

    def generate_batch(self, requests):
        async def _gen():
            q = asyncio.Queue()

            async def worker(req):
                try:
                    msgs = list(req.get("messages", []))
                    if req.get("system_prompt"):
                        msgs = [{"role": "system", "content": req["system_prompt"]}] + msgs
                    async for ev in self.generate(
                        msgs, req.get("sampling_params", {}), req.get("tools")
                    ):
                        await q.put((req.get("request_id", ""), ev))
                except Exception as e:  # noqa: BLE001
                    await q.put((req.get("request_id", ""), {
                        "type": "final", "stop_reason": "STOP_ERROR",
                        "finish_reason": "error", "token": str(e),
                    }))

            tasks = [asyncio.create_task(worker(r)) for r in requests]
            remaining = len(tasks)
            while remaining > 0:
                req_id, event = await q.get()
                yield req_id, event
                if event.get("type") == "final":
                    remaining -= 1
            for t in tasks:
                await t

        return _gen()


def _usage_dict(u):
    return {
        "prompt_tokens": u.get("prompt_tokens", 0),
        "completion_tokens": u.get("completion_tokens", 0),
        "total_tokens": u.get("total_tokens", 0),
    }
```

- [ ] **Step 4: Bỏ skip + chạy toàn bộ test engine để thấy pass**

Trong `tests/test_engines.py`, xóa dòng `@pytest.mark.skip(reason="LlamaBackend chưa tồn tại cho tới Task 6 — bỏ skip ở Task 6")` khỏi `test_registry_llama`.

Run: `cd python-worker; .\.venv\Scripts\python -m pytest tests/test_llama_backend.py tests/test_engines.py -v`
Expected: PASS — kể cả `test_registry_llama` (giờ `LlamaBackend` tồn tại, không còn skip).

- [ ] **Step 5: Commit**

```bash
git add python-worker/worker/engines/llama/backend.py python-worker/tests/test_llama_backend.py
git commit -m "feat(worker): LlamaBackend — OpenAI mapping, tool_calls, batch"
```

---

### Task 7: Script tải GGUF + cài llama-server

**Files:**
- Create: `scripts/download_qwen35.ps1`
- Create: `scripts/download_qwen35.sh`
- Modify: `scripts/setup.ps1`, `scripts/setup.sh`

**Interfaces:**
- Consumes: `huggingface_hub` (Task 1), llama.cpp prebuilt binary.
- Produces: `models/qwen3.5-9b-q4_k_m.gguf` (~6GB); `llama-server` khả dụng trong PATH.

- [ ] **Step 1: Tạo `scripts/download_qwen35.ps1`**

```powershell
# Tải GGUF Qwen3.5-9B Q4_K_M vào models/
# Usage: .\download_qwen35.ps1 [-Repo Qwen/Qwen3.5-9B-GGUF] [-File qwen3.5-9b-q4_k_m.gguf]
param(
    [string]$Repo = "Qwen/Qwen3.5-9B-GGUF",
    [string]$File = "qwen3.5-9b-q4_k_m.gguf"
)
$ErrorActionPreference = "Stop"
$root = Split-Path $PSScriptRoot -Parent
$dest = Join-Path $root "models"
New-Item -ItemType Directory -Force -Path $dest | Out-Null
Write-Host "Downloading $Repo/$File -> $dest"
python -c "from huggingface_hub import hf_hub_download; print(hf_hub_download('$Repo','$File',local_dir=r'$dest'))"
```
> Tên repo/file chính xác xác minh lúc thực thi trên HF (ưu tiên repo chính thức `Qwen/*-GGUF`; fallback community như `bartowski/*` — cùng file Q4_K_M). Nếu tên khác, sửa 2 param mặc định.

- [ ] **Step 2: Tạo `scripts/download_qwen35.sh`** (bản POSIX tương đương)

```bash
#!/usr/bin/env bash
set -euo pipefail
REPO="${1:-Qwen/Qwen3.5-9B-GGUF}"
FILE="${2:-qwen3.5-9b-q4_k_m.gguf}"
DEST="$(dirname "$(dirname "$0")")/models"
mkdir -p "$DEST"
python -c "from huggingface_hub import hf_hub_download; print(hf_hub_download('$REPO','$FILE',local_dir=r'$DEST'))"
```

- [ ] **Step 3: Cài `llama-server`** (Windows, prebuilt CUDA — chọn 1 trong 2)

**Cách A — tải release zip** (khuyến nghị cho RTX 3060/CUDA):
- Vào [llama.cpp releases](https://github.com/ggml-org/llama.cpp/releases), tải bản `*-bin-win-cuda-cu12.4-x64.zip` (có `llama-server.exe`), giải nén, thêm thư mục chứa `llama-server.exe` vào PATH.
**Cách B — pip:**
```powershell
.\.venv\Scripts\python -m pip install llama-cpp-python
# sau đó dùng: python -m llama_cpp.server ... (server khác lệnh llama-server)
# → chỉ dùng nếu không tải được release; cần sửa LlamaServer.cmd theo hướng dẫn log.
```

- [ ] **Step 4: Cập nhật `scripts/setup.ps1` / `setup.sh`**

Thêm vào bước cài python deps: `pip install httpx huggingface_hub` (hoặc đã nằm trong `pip install -e .[dev]`). Thêm comment hướng dẫn chạy `download_qwen35.ps1` và cài llama-server (Step 3).

- [ ] **Step 5: Chạy script tải GGUF**

Run: `cd scripts; .\download_qwen35.ps1`
Expected: file `models/qwen3.5-9b-q4_k_m.gguf` xuất hiện (~6GB).

- [ ] **Step 6: Commit**

```bash
git add scripts/download_qwen35.ps1 scripts/download_qwen35.sh scripts/setup.ps1 scripts/setup.sh
git commit -m "feat(scripts): download Qwen3.5-9B GGUF + cài llama-server"
```

---

### Task 8: E2E thủ công trên GPU — token stream + tool call

**Files:**
- Chạy thủ công, không viết test tự động (cần GPU).

**Interfaces:**
- Consumes: LlamaBackend (Task 6), GGUF + llama-server (Task 7), Go server.

- [ ] **Step 1: Khởi động worker engine llama**

```powershell
cd python-worker
.\.venv\Scripts\python -m worker.server --engine llama --gguf ..\models\qwen3.5-9b-q4_k_m.gguf
```
Expected: log `[llama] llama-server ready at http://127.0.0.1:8081` + `Worker ready on port 50051`.

- [ ] **Step 2: Khởi động Go server (terminal 2)**

```powershell
cd go-server
go run ./cmd/server/
```
Expected: `Worker ready` / Go log kết nối gRPC thành công.

- [ ] **Step 3: Kiểm tra token stream**

```powershell
curl -X POST http://localhost:8080/v1/messages -H "Content-Type: application/json" `
  -d '{"model":"qwen3.5-9b","stream":true,"messages":[{"role":"user","content":"Xin chào, giới thiệu ngắn về bạn."}]}'
```
Expected: SSE token về từng token; cuối có `message_stop`/`[DONE]`. TTFT ghi lại (so sánh 7B nếu cần).

- [ ] **Step 4: Kiểm tra tool call (vá §9.1 cho engine llama)**

Tạo file `G:\STUDY\AI\ai_factory\test.txt` nội dung `hello`. Gửi request yêu cầu đọc file:
```powershell
curl -X POST http://localhost:8080/v1/messages -H "Content-Type: application/json" `
  -d '{"model":"qwen3.5-9b","messages":[{"role":"user","content":"Đọc nội dung file test.txt trong thư mục dự án."}],"tools":[{"name":"read_file","description":"Đọc file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}'
```
Expected: log Go hiện `tool_use` → `execute read_file` → `tool_result` → model trả lời kết quả đọc. Điều này xác nhận agentic loop chạy nhánh tool.

- [ ] **Step 5: Xử lý sự cố nếu có**

- Lỗi `llama-server khởi động thất bại`: xem stderr llama-server (Task 4 in ra), kiểm tra GGUF/kiến trúc model.
- Tool không gọi: model 9B có thể cần prompt khéo hơn — thử thêm "dùng tool read_file để đọc". Nếu llama-server không hỗ trợ tool cho Qwen3.5, ghi vào docs (Task 9) và hạ thành "chưa xác nhận".
- Không commit gì trong task này (chỉ verify).

---

### Task 9: Cập nhật docs (đồng bộ + sửa model 3B→7B)

**Files:**
- Modify: `CLAUDE.md`
- Modify: `CONTEXT.md`
- Modify: `docs/ARCHITECTURE.md`

**Interfaces:**
- Consumes: toàn bộ thay đổi trước.

- [ ] **Step 1: Sửa `CLAUDE.md`**

- **Key Decisions → Model**: đổi "Qwen 2.5 3B Instruct" thành **"Qwen2.5-Coder-7B-Instruct** (default, `--engine transformers`), ghi chú "docs cũ ghi 3B — code chạy 7B từ trước".
- **Thêm mục "Engine selection"**:
```markdown
## Engine selection

Worker hỗ trợ 2 engine, chọn lúc khởi động (mỗi lúc 1 model, 12GB VRAM):

| Flag | Engine | Model | Runtime |
|---|---|---|---|
| `--engine transformers` (default) | TransformersBackend | Qwen2.5-Coder-7B (4-bit NF4) | transformers + bitsandbytes + BPETokenizer |
| `--engine llama` | LlamaBackend | Qwen3.5-9B (GGUF Q4_K_M) | llama-server (llama.cpp) + httpx proxy |

Chi tiết: `docs/superpowers/specs/2026-08-10-qwen35-gguf-engine-design.md`.
```
- **Project Structure**: thêm `worker/engines/` (base, transformers, llama/), `models/`.
- **Running**: thêm cách chạy llama engine:
```bash
cd python-worker && .\.venv\Scripts\python -m worker.server --engine llama --gguf ..\models\qwen3.5-9b-q4_k_m.gguf
```

- [ ] **Step 2: Sửa `CONTEXT.md`**

Thêm glossary:
```markdown
- **Engine Backend**: Interface trong Python worker (`EngineBackend`) tách inference engine khỏi gRPC servicers. Hai implementation: `TransformersBackend` (Qwen2.5-Coder-7B, transformers) và `LlamaBackend` (Qwen3.5-9B, llama-server proxy). Chọn bằng `--engine` lúc khởi động — mô hình "swap engine sau interface".
- **LlamaProxyEngine**: `LlamaBackend` — spawn `llama-server` subprocess, proxy gRPC → OpenAI-compatible `/v1/chat/completions` (SSE). Tool calling native (structured output) → nhánh tool-use của agentic loop hoạt động thật trên engine này (vá §9.1).
```

- [ ] **Step 3: Sửa `docs/ARCHITECTURE.md`**

- §1.1 diagram: đổi "Python worker: InferenceServicer + BatchInferenceServicer / InferenceEngine / BatchEngine" → thêm tầng `EngineBackend` (Transformers | Llama).
- §3 (Python Worker): thêm mục `3.x LlamaBackend` (LlamaServer + LlamaClient + proxy flow).
- §3.2: sửa `MODEL_ID = "Qwen/Qwen2.5-3B-Instruct"` → `"Qwen/Qwen2.5-Coder-7B-Instruct"` (engine.py:29).
- §9.1: ghi chú "nhánh tool-use đã vá cho engine llama (Qwen3.5-9B), vẫn chết trên transformers 7B".
- §12 roadmap: thêm dòng trạng thái cho "Llama engine (Qwen3.5-9B)".
- §8: ghi chú GGUF/llama-server thay vì bnb cho Qwen3.5.

- [ ] **Step 4: Kiểm tra docs nhất quán** (không còn chỗ nào ghi model 3B ngoài lịch sử)

Grep: `cd G:\STUDY\AI\ai_factory; Select-String -Path "CLAUDE.md","CONTEXT.md","docs\ARCHITECTURE.md" -Pattern "3B"` → chỉ còn các đề cập có ngữ cảnh đúng (lịch sử / tokenizer vocab).

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md CONTEXT.md docs/ARCHITECTURE.md
git commit -m "docs: engine selection + sửa model default 3B→7B + LlamaBackend"
```

---

## Self-Review (ghi lại kết quả)

**Spec coverage:**
- §2 module `engines/` → Task 2, 4, 5, 6 ✅
- §3 data flow (single + batch, stop reason, cancel) → Task 6 (generate/generate_batch), Task 3 (wiring) ✅
- §4 tool calling → Task 6 (`tool_choice:"auto"`, tool_calls accumulation), Task 8 E2E ✅
- §5 config/flags → Task 3 (flags), Task 7 (scripts, setup) ✅
- §6 testing (unit CPU + E2E GPU) → Task 2-6 unit, Task 8 E2E ✅
- §7 docs → Task 9 ✅
- §8 out-of-scope (vLLM Phase 2) → không có task, đúng phạm vi ✅

**Placeholder scan:** Không có TBD/TODO. "Xác nhận tên GGUF lúc thực thi" nằm trong comment hướng dẫn (Task 7), không phải placeholder — engineer tự verify tên repo trên HF.

**Type consistency:**
- `get_backend(name, model_id=None, gguf=None, llama_port=8081, llama_bin="llama-server")` — dùng thống nhất Task 2 (registry) ↔ Task 3 (`serve` gọi) ↔ Task 6 (`LlamaBackend(gguf, port, bin)`).
- `LlamaClient(base_url, transport=None)`, `chat_completions(body)` — Task 5 ↔ Task 6 ✅.
- `LlamaServer(gguf, port=8081, bin=...)`, `.start()`, `.stop()`, `.base_url`, `._wait_healthy(timeout)` — Task 4 ↔ Task 6 ↔ test ✅.
- Event dict + `generate_batch` contract — Task 2 (base.py) ↔ Task 6 ✅.
- Proto enum value `EVENT_TOKEN=1`, `EVENT_FINAL=3`, `STOP_END_TURN=1` dùng trong test Task 3 khớp `inference.proto` ✅.

**Lưu ý rủi ro còn để ngỏ (không chặn plan):** kiến trúc Gated DeltaNet + tool support của llama-server cho Qwen3.5-9B chưa xác nhận 100% trước khi chạy E2E — Task 8 ghi rõ cách xử lý khi fail.
