# Design — Engine Qwen3.5-9B qua llama.cpp (GGUF), song song với core transformers

- **Ngày**: 2026-08-10
- **Trạng thái**: Đã duyệt (user duyệt cả 2 phần design)
- **Phạm vi**: Thêm engine thứ hai — **Qwen3.5-9B (GGUF Q4_K_M)** chạy qua **llama-server** — vào python-worker, giữ nguyên core transformers (Qwen2.5-Coder-7B) làm learning vehicle. Go server + proto + agentic loop **không đổi**.

---

## 1. Bối cảnh & quyết định đã chốt

### 1.1 Hiện trạng code (đã xác minh)

- `python-worker/worker/engine.py:29`: `MODEL_ID = "Qwen/Qwen2.5-Coder-7B-Instruct"` — **không phải 3B như docs ghi** (docs lỗi thời, sẽ sửa ở §7).
- Tokenizer tự viết `BPETokenizer` (byte-level BPE) load `vocab.json`/`merges.txt`/`tokenizer_config.json` từ HF cache; ids đặc biệt EOS/PAD (151645/151643) hardcode fallback theo Qwen2.5.
- gRPC: 2 service server-streaming `InferenceService.Generate` + `BatchInferenceService.BatchGenerate`; Go luôn đi đường **batch** (`BatchScheduler` → `BatchGenerate`).
- `BatchEngine` không phát hiện `tool_use` → nhánh tool-use của agentic loop **chết ở runtime** (lỗ hổng §9.1 của `docs/ARCHITECTURE.md`).

### 1.2 Sự thật kỹ thuật về Qwen3.5-9B (đã xác minh từ HF + web)

- Vocab **248,320** (≠ Qwen2.5: 151,643) → `BPETokenizer` tự viết **không phải drop-in**.
- Kiến trúc **Gated DeltaNet + sparse MoE** (~10B params; BF16 ~20GB → không vừa 12GB). bitsandbytes 4-bit **khả năng cao không hỗ trợ** kiến trúc mới.
- Bản quant sẵn chủ yếu là **GGUF cho llama.cpp/Ollama** (model card gắn 438 derivatives).

### 1.3 Quyết định đã chốt (từ brainstorming)

| # | Quyết định |
|---|---|
| D1 | **Drop-in, fallback nếu cần** — ưu tiên giữ stack transformers+bnb, nhưng fallback sang GGUF/llama.cpp khi cần. |
| D2 | **Song song, giữ core 7B** — transformers engine là learning vehicle (roadmap Tuần 5-8), llama engine là tùy chọn "production-like". |
| D3 | **llama.cpp ngay (Approach A)** — llama-server subprocess + Python proxy; **vLLM là Phase 2** (chỉ đổi endpoint). |

---

## 2. Kiến trúc

### 2.1 Tổng quan — "hai engine, một interface"

```
                 (Go server KHÔNG đổi)                     Python worker (thay đổi tối thiểu)
Client → Go → gRPC ──► InferenceServicer ──► EngineBackend (interface mới)
                        BatchInferenceServicer ──►   ├── TransformersBackend (Qwen2.5-Coder-7B, hiện tại)
                                                     └── LlamaBackend (MỚI)  ──► llama-server subprocess
                                                                                (Qwen3.5-9B GGUF Q4_K_M)
```

- Chọn backend lúc **khởi động worker** (12GB không đủ load cả 2 model cùng lúc).
- Proto, Go, agentic loop, batch scheduler: **giữ nguyên hoàn toàn**.
- `LlamaBackend` quản lý `llama-server` subprocess (port riêng **8081**, tránh Go `8080`) và proxy gRPC → OpenAI-compatible HTTP.

### 2.2 Module mới phía Python

```
python-worker/worker/engines/
├── __init__.py            # registry: get_backend(name) → EngineBackend
├── base.py                # class EngineBackend (abstract): generate + generate_batch + lifecycle
├── transformers.py        # TransformersBackend: wrap InferenceEngine + BatchEngine hiện có
└── llama/
    ├── server.py          # LlamaServer: spawn/stop llama-server, chờ /health
    ├── client.py          # LlamaClient: async httpx streaming tới /v1/chat/completions
    └── backend.py         # LlamaBackend: map gRPC ↔ OpenAI, sinh gRPC events
```

`EngineBackend` (base.py) — interface tối thiểu:

```python
class EngineBackend(ABC):
    @abstractmethod
    async def generate(self, messages, sampling_params, tools, cancel_event=None): ...
    @abstractmethod
    def generate_batch(self, requests): ...  # async iterator (req_id, event)
    @abstractmethod
    def load(self): ...
    @abstractmethod
    def unload(self): ...
```

- `server.py` hiện tại: servicers chuyển từ gọi `engine.*`/`batch_engine.*` sang gọi qua `EngineBackend` (interface mỏng, đúng tinh thần "swap engine sau interface").
- `engine.py` / `batch_engine.py` / `model/tokenizer/`: **không sửa** (vẫn là đường transformers).
- Dep mới (pyproject.toml): `httpx>=0.27` (async streaming), `huggingface_hub` (tải GGUF). PyTorch/bnb/transformers không đổi.

---

## 3. Luồng dữ liệu

### 3.1 Generate (single)

```
gRPC GenerateRequest → LlamaBackend.generate()
  → map messages/system_prompt/tools/sampling → OpenAI Chat Completions request
  → POST /v1/chat/completions (stream=true)
  → parse SSE "data: {...}" từng dòng
  → yield: {"type":"token",...} | {"type":"tool_use",...} | {"type":"final", stop_reason, usage}
  → servicer gửi proto event (contract không đổi)
```

- Map message: dùng lại helper `_messages_from_proto` / `_tools_from_proto` (đang nằm trong `server.py`) — chỉnh để trả OpenAI format.
- SamplingParams → OpenAI params: `max_tokens`, `temperature`, `top_p`, `top_k`, `stop_sequences` → `stop`.
- Token count: `usage` trả từ llama-server (`prompt_tokens`/`completion_tokens`); heuristic `chars/4` phía Go không đổi.

### 3.2 BatchGenerate

- Mỗi request trong batch chạy **1 async HTTP streaming call riêng** → `yield (request_id, event)` (đúng chữ ký `BatchEngine.generate_batch` hiện tại).
- llama-server tự **continuous batching** phía sau → Go không cần chế batching tay.
- Lỗi/timeout một request → gửi `STOP_ERROR` cho request đó, không ảnh hưởng request khác.

### 3.3 Stop reason & cancel

| `finish_reason` (llama-server) | `stop_reason` (proto) |
|---|---|
| `stop` | `STOP_END_TURN` |
| `length` | `STOP_MAX_TOKENS` |
| `tool_calls` | `STOP_TOOL_USE` |
| client disconnect | `STOP_CANCELLED` |
| HTTP/parse lỗi | `STOP_ERROR` |

- **Cancel:** gRPC context bị hủy → abort HTTP stream ngay (đơn giản hơn poll 100ms của đường transformers). Không cần `cancel_event` poll.

---

## 4. Tool calling (vá lỗ hổng §9.1 cho engine llama)

- Truyền `tools` (JSON schema từ proto `ToolDefinition`) + `tool_choice: "auto"` vào llama-server → model trả `tool_calls` có cấu trúc → parse thành proto `EVENT_TOOL_USE` (+ `id`, `name`, `arguments`).
- **Nhánh tool-use của agentic loop sẽ hoạt động thật** trên đường llama. Go không phải sửa gì.
- ⚠️ Giới hạn: điều này chỉ vá cho engine llama; đường transformers (7B) vẫn còn lỗ hổng — ngoài phạm vi spec này.

---

## 5. Cấu hình & cách chạy

- **Worker flags mới** (`server.py` argparse):
  - `--engine transformers|llama` (default `transformers`)
  - `--gguf <path|repo>` (chỉ cần khi `--engine llama`)
  - `--llama-port 8081`
  - `--llama-bin <path>` (default: `llama-server` trong PATH)
  - Env fallback: `AI_FACTORY_ENGINE`, `AI_FACTORY_GGUF`.
- **GGUF**: script `scripts/download_qwen35.ps1/.sh` dùng `huggingface_hub` tải **Q4_K_M (~6GB)** vào `models/`. Ưu tiên repo `Qwen/Qwen3.5-9B-GGUF`; fallback community (vd `bartowski/*`) — xác nhận lúc implement.
- **llama-server binary**: cài prebuilt từ bản release của llama.cpp cho Windows (không cần build) vào `scripts/` hoặc PATH.
- **llama-server flags**: `-m <gguf> --host 127.0.0.1 --port 8081 --n-gpu-layers -1 --ctx-size 8192 --threads 8`.
- **Cách chạy** (2 terminal, worker tự spawn llama-server):
  ```bash
  cd python-worker && python -m worker.server --engine llama --gguf models/qwen3.5-9b-q4_k_m.gguf
  cd go-server && go run ./cmd/server/
  ```
- **Scripts setup**: `scripts/setup.ps1/.sh` — cài thêm `httpx`, `huggingface_hub`, llama-server binary.

---

## 6. Testing

| Cấp | Nội dung | Yêu cầu |
|---|---|---|
| **Unit** `tests/test_llama_client.py` | Map proto→OpenAI request; parse SSE `data:`; map `finish_reason`; parse `tool_calls` → `EVENT_TOOL_USE` | CPU, mock HTTP (`respx` hoặc giả bằng tay) |
| **Unit** `tests/test_engines.py` | Registry trả đúng backend theo `--engine`; `LlamaServer.start/health/stop` với binary giả | CPU |
| **E2E** (thủ công) | Worker `--engine llama` + Go: (1) token stream, (2) request có tool → xác nhận tool-use | GPU |
| **Benchmark** (tùy chọn) | `benchmark.py` cho 9B-GGUF → cập nhật `docs/BENCHMARK.md` (TTFT/TPOT/throughput so 7B) | GPU |

- ⚠️ **Tiền đề**: môi trường dev hiện chưa có venv; Python mặc định thiếu `transformers` → `pytest --collect-only` fail. Plan phải bắt đầu bằng **tạo venv + cài deps** (`python -m venv .venv` + `pip install -e .[dev]`).

---

## 7. Cập nhật docs

- **`CLAUDE.md`**: thêm mục "Engine selection"; cây thư mục worker thêm `engines/`; cách chạy llama engine; **sửa model default → Qwen2.5-Coder-7B** (docs hiện ghi 3B, code chạy 7B).
- **`CONTEXT.md`**: thêm glossary `Engine Backend`, `LlamaProxyEngine`.
- **`docs/ARCHITECTURE.md`**: thêm mục `LlamaBackend` (§3.x), sửa `MODEL_ID`, cập nhật §12 roadmap.
- Ghi chú rõ: llama engine = "production-like"; transformers engine = "learning vehicle" cho Tuần 5-8.

---

## 8. Ngoài phạm vi (Phase sau)

- **vLLM (WSL2)**: chỉ viết tài liệu + để sẵn cấu hình endpoint; không code trong plan này. Vì worker là proxy tới endpoint OpenAI-compatible, đổi sang vLLM chỉ là đổi endpoint.
- **Roadmap Tuần 5-8** (sampling tự viết, KV cache): tiếp tục trên core transformers 7B; không đụng llama path.
- **Không** đổi proto; **không** đổi Go; **không** tự quant GGUF; **không** vá §9.1 cho đường transformers.

---

## 9. Rủi ro & xử lý

| Rủi ro | Xử lý |
|---|---|
| llama-server chưa hỗ trợ Qwen3.5-9B arch | Chọn GGUF version tương thích; llama.cpp release mới hỗ trợ Qwen3.5 (model card gắn derivatives). Test `/health` + 1 prompt ngắn trước khi tích hợp |
| Không có repo GGUF chính thức 9B | Fallback community (bartowski); Q4_K_M tương đương nhau |
| Map tool_calls lệch format | Unit test parse từ fixture SSE thật; dùng `tools` format chuẩn OpenAI |
| httpx phụ thuộc mới | Version pin trong pyproject; test CPU chạy được không cần GPU |
| Load đồng thời 2 model → OOM | Backend chọn lúc khởi động, mỗi lúc chỉ load 1 |
| Worker spawn llama-server fail (missing binary/GGUF) | Lỗi rõ ràng lúc `load()`; hướng dẫn trong log (chạy script download) |
| Windows + subprocess PATH | Dùng đường dẫn tuyệt đối tới binary từ `--llama-bin` (default: `llama-server` trong PATH) |

---

## 10. Mở / câu hỏi để ngỏ

- Repo GGUF chính thức hay community cho Qwen3.5-9B Q4_K_M (xác nhận lúc implement).
- Có muốn tự động tải GGUF khi `--gguf` là repo id (thay vì yêu cầu script riêng) không — quyết định lúc implement, mặc định script riêng.
- Benchmark 9B vs 7B có phải deliverable bắt buộc của plan này không (mặc định: tùy chọn).
