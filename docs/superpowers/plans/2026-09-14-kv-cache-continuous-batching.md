# KV cache + Continuous Batching — Implementation Plan

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** [`docs/superpowers/specs/2026-09-14-kv-cache-continuous-batching-design.md`](../specs/2026-09-14-kv-cache-continuous-batching-design.md) (D1–D7).

**Goal:** Thay `BatchEngine` (static batching, HF quản `past_key_values`) bằng engine **continuous batching** tự viết, với **KV cache tự quản** (storage + assembly + eviction). Chỉ engine `transformers`; proto giữ nguyên; Go chỉ nới số batch in-flight.

**Scope:** `python-worker/worker/**` + `python-worker/tests/**` (data plane) + config/wiring Go tối thiểu. Không đổi proto, không đổi `LlamaBackend`, không viết lại forward pass, không PagedAttention/chunked prefill.

**Tech:** Python 3 + torch + transformers (không thêm dependency); Go 1.25.7 module `github.com/ai-factory/go-server`.

---

## Decisions (locked)

Kế thừa D1–D7 của spec. Nhắc lại các điểm ảnh hưởng trực tiếp tới code:

- **D1** — Ta sở hữu per-sequence cache (perspective legacy-tuple `[1,H,S,D]`); HF chỉ chạy attention 1 bước. Tự append K/V + eviction.
- **D2** — Proto không đổi; scheduler Python iteration-level; Go nới `max_in_flight_batches`.
- **D3** — Chỉ `TransformersBackend`; `LlamaBackend` nguyên vẹn.
- **D4** — Single `Generate` cũng đi qua scheduler (một luồng sở hữu model).
- **D5** — Pad trái + `position_ids` tường minh khi assemble batch nhiều độ dài.
- **D6** — Không chunked prefill.
- **D7** — Giữ thuật toán Go + Batch Slot/load shedding; chỉ đổi số in-flight.

## Global Constraints

- Test CPU-only: fake model trong `tests/fake_model.py` mô phỏng interface HF (`__call__(input_ids, attention_mask, position_ids, past_key_values, use_cache) -> .logits + .past_key_values`).
- Không log prompt/raw key; không thêm dependency.
- TDD: test fail trước → implement → pass. Chạy `python -m pytest tests/`.
- Contract `backend.generate_batch(requests) -> AsyncIterator[(request_id, event)]` **không đổi** (server.py không cần sửa).
- Commit style: `feat(inference): ...`, `chore(inference): ...`, `feat(load): ...`, `docs(spec): ...`.
- Verify cuối: `cd python-worker && python -m pytest tests/` + `cd go-server && go vet ./... && go test ./...`.

---

## Task 1: KV cache + batch assembly (pure, CPU)

**Files:** `python-worker/worker/kv_cache.py` (new), `python-worker/tests/test_kv_cache.py` (new).

- [ ] Test: `KVCache.attach(out_past, prompt_len)` lưu tuple per-layer, `length == prompt_len`.
- [ ] Test: `KVCache.append_from_output(out_past, row)` lấy đúng slot cuối (`[..., -1:, :]`), `length += 1`, không giữ pad.
- [ ] Test: `KVCache.free()` drop tensor + `length = 0`.
- [ ] Test: `build_prefill(seqs)` — left-pad `input_ids`/`attention_mask`, `position_ids` = 0..P-1 cho vùng hợp lệ (pad region masked).
- [ ] Test: `build_decode(active)` — `input_ids` `[B,1]`, `position_ids[i] == seq.length`, `attention_mask` `[B, L+1]` (0 ở pad trái, 1 vùng hợp lệ + token mới), past tuple pad trái tới `L` per-layer.
- [ ] Implement `KVCache` (buffer + `length`/`capacity`, growth ×2) và `KVCacheManager` (`build_prefill`, `build_decode`, `append_from_output`, `total_tokens`).
- [ ] `python -m pytest tests/test_kv_cache.py` xanh; `python -m pytest tests/` không hồi quy.

## Task 2: Fake HF model (test double)

**Files:** `python-worker/tests/fake_model.py` (new), `python-worker/tests/test_fake_model.py` (new).

- [ ] Implement fake model: mỗi layer cache `(k, v)` shape `[B, 1, S, 1]` giữ `input_ids`; `logits[:, -1, :]` suy từ tổng history (past + current) → **sai assembly sẽ đổi output**.
- [ ] Hỗ trợ `past_key_values=None`, `position_ids`, `use_cache`; trả object có `.logits` + `.past_key_values`.
- [ ] Test sanity: chạy không cache (full prompt mỗi bước) vs có cache → logits khớp (chứng minh double dùng đúng).
- [ ] `python -m pytest tests/test_fake_model.py` xanh.

## Task 3: Continuous scheduler

**Files:** `python-worker/worker/continuous_batch_engine.py` (new), `python-worker/tests/test_continuous_batch.py` (new).

- [ ] `Sequence` (request_id, prompt_ids, params, stop_ids, stop_sequences, decoder, rpc_queue, cache, generated, finished, cancelled).
- [ ] `ContinuousBatchEngine(model, tokenizer, hf_tokenizer, max_batch_size=4, max_batch_tokens=8192)`:
  - daemon thread + `threading.Condition`; `waiting` deque, `active` list.
  - vòng lặp: `admit()` (budget) → prefill batch hoặc `cond.wait(timeout)` khi rỗng → `decode_step(active)` (1 forward) → `evict_finished()`.
  - tái sử dụng `sample_next_batch` (`sampling.py`), không sửa toán sampling.
  - `submit(seqs)` thread-safe + notify; `stop()` để shutdown.
- [ ] `generate_batch(requests) -> AsyncIterator[(request_id, event)]`: per-call `stdlib_queue.Queue`, drain bằng `asyncio.to_thread(q.get, True, 0.2)` (pattern hiện có), yield tới khi đủ `final`.
- [ ] Test: 2 request cùng lúc → cả hai `final` đúng, token khớp per-sequence reference.
- [ ] Test: **continuous** — submit A, chạy vài bước rồi submit B → B phát token đầu **trước khi** A `final`.
- [ ] Test: `max_tokens`, stop id → kết thúc đúng; `final` usage đúng (`prompt_tokens`/`completion_tokens`).
- [ ] Test: cancel (`cancel_event`/cờ) → `STOP_CANCELLED` + evict + `cache.free()`.
- [ ] Test: budget `max_batch_tokens` giới hạn số admit/iteration.
- [ ] `python -m pytest tests/test_continuous_batch.py` xanh.

## Task 4: Prompt builder dùng chung

**Files:** `python-worker/worker/prompt.py` (new), `python-worker/worker/continuous_batch_engine.py`, (tuỳ chọn) `python-worker/worker/batch_engine.py`.

- [ ] Trích `build_chat_prompt(hf_tokenizer, messages, tools) -> str` từ `BatchEngine._build_prompt` (giữ nguyên hành vi + fallback khi template lỗi).
- [ ] Continuous engine dùng hàm này (không copy code).
- [ ] Test: prompt tạo ra giống hệt `BatchEngine._build_prompt` cho cùng input (parity).

## Task 5: `stop_sequences` trong batch path

**Files:** `python-worker/worker/continuous_batch_engine.py`, `python-worker/tests/test_continuous_batch.py`.

- [ ] Áp `stop_sequences` như đường single (`engine.py:251`): cắt tại chuỗi dừng, chỉ phát text trước nó, kết thúc `STOP_END_TURN`.
- [ ] Test: prompt sinh ra chuỗi chứa stop sequence → token sau stop không phát, phần trước được phát đủ.

## Task 6: Wire `TransformersBackend` + single path

**Files:** `python-worker/worker/engines/transformers.py`, `python-worker/worker/engines/base.py`, `python-worker/tests/test_engines.py`.

- [ ] `TransformersBackend` lazily tạo `ContinuousBatchEngine`; `generate_batch` uỷ quyền thẳng (bỏ `_guard` bọc-trọn-batch).
- [ ] Single `generate` (D4): submit một request vào scheduler, map event; truyền `cancel_event` vào request dict.
- [ ] `base.py`: giữ `_gen_lock` cho `LlamaBackend`; transformers không dùng.
- [ ] Cập nhật `tests/test_engines.py` với fake engine để kiểm tra contract event.
- [ ] `python -m pytest tests/` xanh (bỏ qua 4 test `test_llama_backend.py` fail sẵn).

## Task 7: Go — nới in-flight batch

**Files:** `go-server/internal/infrastructure/inference/batch_scheduler.go`, `batch_scheduler_test.go`, `go-server/internal/config/config.go`, `config_test.go`, `go-server/configs/config.yaml`, `go-server/internal/app/registry.go`.

- [ ] Test first: `SetMaxInFlightBatches(n)` — với `n=2`, 2 batch dispatch đồng thời; `n=1` giữ hành vi cũ.
- [ ] Thêm `SetMaxInFlightBatches(n int)` + guard `n ≥ 1`.
- [ ] Config `InferenceMaxInFlightBatches` (env `AI_FACTORY_INFERENCE_MAX_IN_FLIGHT_BATCHES`, default **4**) + `configs/config.yaml`.
- [ ] Wiring trong `registry.go`: gọi setter khi dựng `batch.scheduler`.
- [ ] `go vet ./...`, `go test ./...` xanh.

## Task 8: Verification + cleanup + docs

- [ ] Parity test cuối: greedy, cùng input → output continuous engine == `BatchEngine` cũ (trên fake model).
- [ ] Manual GPU (ngoài CI): so TTFT + throughput tok/s static vs continuous (batch 4); đo cache tokens để lo VRAM (12GB/7B 4-bit); điều chỉnh `max_batch_tokens` nếu cần.
- [ ] Xoá `python-worker/worker/batch_engine.py` (và import/test liên quan) sau khi parity xanh.
- [ ] `python -m pytest tests/` + `go vet ./...` + `go test ./...` sạch.
- [ ] Cập nhật `CLAUDE.md`, `docs/TRACKING.md` (giai đoạn 4 ✅), `docs/LEARNING_ROADMAP.md` (B4 ✅), `docs/ARCHITECTURE.md` (mô tả continuous + KV tự quản).
- [ ] Commit theo cụm: engine Py → Go config → docs.

---

## Self-Review Checkpoints

- [ ] Sequence mới vào được **giữa lúc** sequence khác đang decode (test continuous chứng minh).
- [ ] Chỉ scheduler thread gọi `model(...)`; single + batch không đụng model cùng lúc.
- [ ] KV cache được free khi sequence xong/cancel (không rò VRAM).
- [ ] Pad trái + `position_ids` cho output **giống hệt** per-sequence (parity test).
- [ ] `stop_sequences`/`max_tokens`/stop id kết thúc đúng trên cả batch.
- [ ] Proto, `LlamaBackend`, thuật toán Go (collector/Slot/load shedding) **không đổi**.
- [ ] Không thêm dependency; `python -m pytest tests/` + `go test ./...` xanh.

## Definition of Done

Continuous engine là đường batch duy nhất của `TransformersBackend`, parity với static engine cũ trên test CPU, verify thật trên GPU có số đo throughput/TTFT, docs + roadmap cập nhật, `batch_engine.py` đã xoá.
