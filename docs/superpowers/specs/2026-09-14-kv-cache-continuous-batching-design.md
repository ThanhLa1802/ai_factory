# Design — Tuần 7–8: Tự quản lý KV cache + Continuous Batching (transformers)

- **Ngày**: 2026-09-14
- **Trạng thái**: Draft — chờ user duyệt spec trước khi lập plan
- **Phạm vi**: Thay `BatchEngine` (static batching, HF quản `past_key_values`) bằng một engine **continuous/dynamic batching** do dự án tự viết, với **KV cache tự quản** (storage + assembly + eviction). Chỉ áp cho engine `transformers` (Qwen2.5-Coder-7B). Proto gRPC **giữ nguyên**; Go chỉ đổi cấu hình số batch in-flight.
- **Liên hệ**: nối tiếp Tuần 5–6 (`worker/sampling.py`, spec `2026-08-08-tokenizer-design.md`); map roadmap `docs/LEARNING_ROADMAP.md` Track B giai đoạn B4; tiến độ `docs/TRACKING.md` giai đoạn 4.

---

## 1. Bối cảnh & quyết định đã chốt

### 1.1 Hiện trạng đã xác minh (từ code)

- **Đường production dùng `BatchGenerate`**: agentic loop gọi `scheduler.TrySubmit` (`loop.go:144`) → `BatchScheduler` gom 100ms, batch ≤ 4 → gRPC `BatchInferenceService.BatchGenerate` (`batch_scheduler.go:255,270`). RPC `Generate` (single) **không được Go dùng** trong đường này.
- **Static batching**: `BatchScheduler` giữ `DefaultMaxInFlightBatches = 1` (`batch_scheduler.go:27`) — chỉ 1 batch bay tại một thời điểm, batch sau đợi batch trước xong ⇒ không có request mới vào được khi sequence khác đang decode.
- **BatchEngine dùng HF quản KV cache**: `generate_tokens` (`sampling.py:130`) truyền `past_key_values` và để HF append; `BatchEngine.generate_batch` (`batch_engine.py:86`) tokenize **pad phải** (`build_inputs`, `bpe.py:226`), chạy **một** vòng lặp cho **toàn bộ** rows tới khi mọi row xong.
- **Lãng phí & giới hạn**: row đã `done` vẫn được đưa vào forward pass mỗi bước và vẫn được sample (`sampling.py:174-188` chỉ bỏ yield, không bỏ khỏi compute); tập sequence cố định từ đầu, không thể chèn/xoá giữa chừng; không hỗ trợ `stop_sequences` (chỉ đường single `engine.py:251` có).
- **Serialize model**: `EngineBackend._gen_lock` (`base.py:17`) bọc trọn một batch (`transformers.py:35`) để single và batch không đụng model cùng lúc.
- **Sampling đã tách hàm thuần**: `sample_next`/`sample_next_batch`/`apply_*` (`sampling.py`) chạy trên tensor `[B, V]` → **tái sử dụng được**, không phụ thuộc cách HF quản cache.

### 1.2 Quyết định đã chốt (brainstorming)

| # | Quyết định |
|---|---|
| D1 | **"Tự quản KV cache" = tự sở hữu storage + assembly + eviction**, không viết lại forward pass. HF chỉ chạy attention cho **một bước**; ta giữ buffer per-sequence, gather thành batch mỗi bước, tự append K/V, tự giải phóng. Không đụng attention/KV-append thủ công (để Tuần 9+). |
| D2 | **Tích hợp Python-only**: scheduler iteration-level sống trong worker, **protol không đổi**; Go chỉ nới `DefaultMaxInFlightBatches` (config, vẫn bounded) để nhiều batch cùng bay. |
| D3 | **Chỉ engine `transformers`**. `LlamaBackend` giữ nguyên (llama.cpp đã tự continuous batching). |
| D4 | **Single `Generate` cũng đi qua scheduler** để đảm bảo chỉ một luồng điều khiển model (thay `_gen_lock` trên batch); đơn giản hoá ownership. |
| D5 | **Pad trái + `position_ids` tường minh** khi assemble batch nhiều độ dài; dùng định dạng cache tuple (legacy) của HF để assemble được. |
| D6 | **MVP không chunked prefill**: prefill trọn prompt một lần khi admit; chunked prefill là refinement sau (non-goal). |
| D7 | **Giữ khái niệm Batch Slot của Go** (load shedding) — chỉ tăng số in-flight, không thay thuật toán scheduler Go. |

---

## 2. Kiến trúc

```
Go BatchScheduler (maxInFlight = N ≥ 2)
   │  mỗi batch: RPC BatchGenerate(requests[≤4])
   ▼
BatchInferenceServicer.BatchGenerate            [server.py — không đổi ]
   │  backend.generate_batch(batch_requests)  → AsyncIterator[(request_id, event)]
   ▼
TransformersBackend.generate_batch              [transformers.py — đổi ]
   │  enqueue từng request vào ContinuousBatchEngine, stream event của RPC này
   ▼
ContinuousBatchEngine (1 daemon thread, sở hữu model)   [MỚI]
   ├─ waiting ──admit──► active
   ├─ prefill(admit)  ─┐
   ├─ decode-step ─────┼─► KVCacheManager: assemble padded batch + position_ids/mask
   ├─ sample (sampling.py)              │
   └─ evict/free ◄──────────────────────┘
   → đẩy (request_id, event) về queue của RPC tương ứng
```

Bất biến: **chỉ scheduler thread gọi `model(...)`**. RPC handlers chỉ enqueue và đọc event queue.

---

## 3. KV cache tự quản

### 3.1 Cấu trúc

**File mới**: `python-worker/worker/kv_cache.py`.

```python
class KVCache:
    """KV cache của MỘT sequence, layout legacy-tuple [1, H_kv, S, D] mỗi layer."""
    layers: list[tuple[Tensor, Tensor]]   # (k, v) mỗi layer
    length: int                           # số vị trí hợp lệ
    capacity: int                         # S hiện tại của buffer (cấp phát theo growth)

    def append_from_output(self, out_past, row: int) -> None
    def free(self) -> None
```

- Prefill: gọi `model(..., past_key_values=None, use_cache=True)` → nhận `out.past_key_values`, chuyển sang legacy tuple (`DynamicCache.to_legacy_cache()` nếu cần, tuỳ version HF), lưu làm cache của sequence; `length = prompt_len`.
- Decode: sau forward, trích **slot cuối** `out_past[l][row, :, -1:, :]` append vào `cache.layers[l]`, `length += 1`. Không giữ phần pad.
- Cấp phát: buffer tăng theo bội số (geometric, ví dụ ×2) để tránh realloc mỗi token; `capacity ≥ max_len` của request.

### 3.2 Assemble batch nhiều độ dài (D5)

`KVCacheManager.build_step(active: list[Sequence]) -> StepInputs`:

1. `L = max(seq.cache.length)` trên các active.
2. Với mỗi layer, `cat` K/V của các sequence theo **trục batch**, **pad trái** cho bằng `L` (zero).
3. `attention_mask` shape `[B, L + q_len]`: vùng pad = 0, vùng hợp lệ = 1, token mới = 1.
4. `position_ids` shape `[B, q_len]`: với decode `q_len=1`, `position_ids[i] = seq.cache.length` (vị trí thật của token mới, để RoPE đúng); với prefill là `arange(P)`.
5. `input_ids`: decode = token cuối mỗi seq (`[B,1]`); prefill = prompt **pad trái** (`[B,P]`).

Sau `out = model(...)`, mỗi row nhận K/V mới ở slot cuối; gọi `KVCache.append_from_output(out.past_key_values, i)`.

> Chú ý RoPE (Qwen): nếu không truyền `position_ids`, HF suy ra từ cumsum attention_mask; với pad trái thì vị trí vẫn đúng nhưng **bắt buộc kiểm thử parity** (test dưới) vì đây là chỗ dễ sai nhất.

### 3.3 Vòng đời

`prefill → active → (decode*) → evict → free`. `free()` drop tensor; gọi `torch.cuda.empty_cache()` **định kỳ** (ví dụ khi batch rỗng), không mỗi bước (tránh sync).

---

## 4. Continuous scheduler

**File mới**: `python-worker/worker/continuous_batch_engine.py`.

### 4.1 Dữ liệu

```python
class Sequence:
    request_id: str
    prompt_ids: Tensor          # [P] (chưa pad)
    params: SamplingParams
    stop_ids: set[int]
    stop_sequences: list[str]
    decoder: StreamingDecoder
    rpc_queue: stdlib_queue.Queue   # queue của RPC đã submit
    cache: KVCache | None
    generated: int
    finished: bool
    cancelled: bool
```

### 4.2 Vòng lặp (daemon thread + Condition)

```
while running:
    admit()                     # waiting → active (theo budget), chạy prefill batch
    if not active:
        cond.wait(timeout=5ms)  # hoặc chờ notify khi có submit
        continue
    logits = decode_step(active)     # 1 forward pass cho toàn bộ active
    for seq, tid in zip(active, sample_next_batch(logits, params)):
        emit token / phát hiện stop | max_tokens | stop_sequences
        append cache; nếu xong → evict + gửi final
```

- **Admission budget**: `max_batch_size` (số sequence active, default 4, khớp Go) và `max_batch_tokens` (tổng `prompt_len + max_tokens` dự kiến, default 8192) để bound VRAM. Request đơn lẻ vượt budget vẫn được nhận (chạy một mình).
- **Prefill batch**: gom các sequence vừa admit trong cùng iteration, assemble như 3.2 với cache rỗng; sample token đầu tiên ngay trong bước prefill.
- **Continuous**: mỗi iteration đều thử admit → sequence mới vào giữa lúc sequence cũ đang decode (đây là điểm khác căn bản so với static hiện tại).
- **Eviction**: gửi `final` (`STOP_END_TURN`/`STOP_MAX_TOKENS`/`STOP_TOOL_USE`/`STOP_CANCELLED`), `cache.free()`, xoá khỏi active.
- **Cancel**: RPC đóng/cancel → đánh dấu `seq.cancelled` → eviction kế tiếp phát `STOP_CANCELLED`.
- **Idle**: active rỗng và không có waiting → `Condition.wait` (không busy-spin).

### 4.3 Giao event về RPC

- Mỗi `BatchGenerate` giữ một `stdlib_queue.Queue` riêng; mỗi request của RPC trỏ vào queue đó.
- Scheduler thread `put((request_id, event))` (thread-safe); async generator của RPC `await asyncio.to_thread(q.get, True, 0.2)` rồi yield — **giữ đúng pattern hiện có** (`batch_engine.py:198`), không cần `call_soon_threadsafe`.
- RPC kết thúc khi đã nhận đủ `final` cho mọi request của nó. Queue **unbounded** (MVP); backpressure là risk đã biết (§9).

---

## 5. Tích hợp Go (chỉ nới in-flight)

- **`batch_scheduler.go`**: thêm `SetMaxInFlightBatches(n int)` (hiện `maxInFlight` chỉ set trong constructor, `:93`).
- **`config`**: thêm `inference.max_in_flight_batches` (env `AI_FACTORY_INFERENCE_MAX_IN_FLIGHT_BATCHES`, default **4**) + `inference.max_batch_size` (env, default 4, hiện qua flag `--max-concurrent`).
- **Wiring** (`internal/app/registry.go`): gọi setter khi dựng `batch.scheduler`.
- **Không đổi**: thuật toán collector, `batchWindow` 100ms, Batch Slot + `TrySubmit`/load shedding (D7), proto, metrics.

> Lý do: scheduler Python serialize forward pass, nên nhiều RPC in-flight chỉ làm **hàng đợi sequence dài hơn** — đúng điều kiện để continuous batching phát huy. Số in-flight vẫn bounded nên tinh thần C1–C4 giữ nguyên.

---

## 6. Config / logging / metrics (Python)

- Env/args: `AI_FACTORY_MAX_BATCH_SIZE` (default 4), `AI_FACTORY_MAX_BATCH_TOKENS` (default 8192), `AI_FACTORY_CACHE_GROWTH` (default 2).
- Log mỗi batch (thay `print` hiện tại): `active`, `waiting`, `total_cache_tokens`, `throughput tok/s`, `ttft_ms`.
- (Tuỳ chọn) đếm cache token để biết áp lực VRAM.

**Non-goal**: chưa gắn Prometheus cho worker (worker hiện log-only).

---

## 7. Scope cut / non-goals

- ❌ Không viết lại forward pass / attention / KV-append thủ công (để Tuần 9+).
- ❌ Không PagedAttention, không prefix caching, không speculative decode.
- ❌ Không chunked prefill (D6).
- ❌ Không đổi proto, không đổi `LlamaBackend`, không đổi thuật toán Go.
- ❌ Không multi-worker / GPU pool / sharding.
- ⚠️ `stop_sequences` hiện chỉ có ở đường single; continuous engine **sẽ hỗ trợ** để parity (coi là bug-fix kèm theo).
- ⚠️ `batch_engine.py` cũ: giữ tạm để tham chiếu, ngừng dùng sau khi continuous engine xanh; xoá ở bước cuối.

---

## 8. Testing

Test CPU-only (theo pattern `tests/test_sampling.py`), dùng **fake model** giả lập interface HF (`__call__(input_ids, attention_mask, position_ids, past_key_values, use_cache) -> .logits + .past_key_values`).

1. **`test_kv_cache.py`** — assembly:
   - Pad trái + `position_ids` + mask tạo đúng input shape.
   - `append_from_output` tăng `length`, lấy đúng slot cuối.
   - `free()` giải phóng.
2. **`test_continuous_batch.py`** — scheduler:
   - Greedy: output của engine continuous **khớp** static/per-sequence (parity).
   - `max_tokens`/`stop`/`stop_sequences` kết thúc đúng sequence.
   - **Continuous**: submit A, chạy vài bước rồi submit B → B phát token đầu **trước khi** A xong.
   - Eviction giải phóng cache; admission tuân budget `max_batch_tokens`.
   - Cancel → `STOP_CANCELLED` + evict.
   - Idle không busy-spin (thread chờ).
3. **`test_engines.py`** (cập nhật): `TransformersBackend.generate_batch` vẫn trả `(request_id, event)` đúng contract.
4. **Parity với static cũ**: cùng input + greedy → text giống `batch_engine` cũ (chạy trên fake model, không cần GPU).
5. **`python -m pytest tests/`** xanh (4 test `test_llama_backend.py` fail sẵn từ trước — không liên quan).
6. **Verify thủ công trên GPU** (ngoài CI): TTFT, throughput tok/s, so sánh static vs continuous ở batch 4; log cache token để phát hiện OOM.

---

## 9. Risks / open questions

- **Định dạng cache HF**: version transformers hiện tại có thể trả `DynamicCache` thay tuple; cần xác nhận `model(..., past_key_values=<tuple>)` còn nhận không, hoặc dùng `.from_legacy_cache()`. Xử lý ở `KVCache`.
- **VRAM (12GB, 7B 4-bit)**: pad cache tới `max` + nhiều sequence có thể OOM. Giảm mặc định (`max_batch_tokens`), đo trên máy thật trước khi tăng.
- **Đúng RoPE với pad trái**: rủi ro sai số nếu thiếu `position_ids`; test parity bắt buộc.
- **Backpressure slow client**: queue per-RPC unbounded; nếu client chậm, RAM tăng. MVP chấp nhận; refinement: giới hạn queue + ngắt sequence.
- **Thread/loop bridge & shutdown**: đảm bảo scheduler thread dừng gọn khi worker SIGTERM, cancel hết sequence, `free()` cache.
- **Chi phí gather/pad mỗi bước**: batch nhỏ (≤4–8) nên chấp nhận; nếu chậm sẽ nhóm sequence cùng độ dài.

---

## 10. Files touched

**Python (data plane)**
- `python-worker/worker/continuous_batch_engine.py` — **MỚI**: scheduler + `Sequence`.
- `python-worker/worker/kv_cache.py` — **MỚI**: `KVCache` + `KVCacheManager` (assemble).
- `python-worker/worker/sampling.py` — tái sử dụng `sample_next_batch` (có thể thêm helper decode 1 bước).
- `python-worker/worker/engines/transformers.py` — dùng continuous engine; bỏ `_gen_lock` theo-batch; route cả single qua scheduler (D4).
- `python-worker/worker/engines/base.py` — điều chỉnh ownership/lock (D4).
- `python-worker/worker/batch_engine.py` — ngừng dùng, xoá ở bước cuối.
- `python-worker/worker/server.py` — không đổi contract (kiểm tra lại đường cancel/drain).
- `python-worker/tests/test_kv_cache.py`, `tests/test_continuous_batch.py` — **MỚI**; `tests/test_engines.py` — cập nhật.
- `python-worker/benchmark.py` — thêm chế độ đo continuous (tuỳ chọn).

**Go (control plane)**
- `go-server/internal/infrastructure/inference/batch_scheduler.go` — `SetMaxInFlightBatches`.
- `go-server/internal/config/config.go` + `configs/config.yaml` — `inference.max_in_flight_batches`, `inference.max_batch_size`.
- `go-server/internal/app/registry.go` — wiring setter.
- `go-server/internal/infrastructure/inference/batch_scheduler_test.go` — test setter/in-flight.

**Docs**
- `CLAUDE.md`, `docs/TRACKING.md`, `docs/LEARNING_ROADMAP.md`, `docs/ARCHITECTURE.md` — đánh dấu giai đoạn 4 xong, cập nhật mô tả batching/KV cache.
