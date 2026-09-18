# Prefix Caching — Implementation Plan (Tuần 9+, Phase B)

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** [`docs/superpowers/specs/2026-09-18-self-written-forward-pass-design.md`](../specs/2026-09-18-self-written-forward-pass-design.md) §5 (D8).

**Goal:** Tái dùng KV của **prefix chung** giữa các request (system prompt, các block prompt giống nhau, lịch sử hội thoại) để bỏ prefill lặp → giảm TTFT. Chỉ engine `transformers`; proto/Go/`LlamaBackend` không đổi.

**Scope:** `python-worker/worker/prefix_cache.py` (mới) + `worker/continuous_batch_engine.py` (tích hợp) + `worker/kv_cache.py` (helper nếu cần) + tests. Không PagedAttention (Phase C), không CUDA kernel.

**Tech:** Python 3.12 + torch (không thêm dependency).

---

## Decisions (locked)

- **B1 — Block-aligned exact-prefix reuse.** Chia token ids thành block `BLOCK_SIZE` (mặc định 16). Block chỉ được cache khi **đủ**. Khoá = `hash(parent_hash, block_token_ids)` (chuỗi hash theo radix) → trả **longest prefix match**.
- **B2 — Copy-on-adopt, chưa paging.** Khi match, **copy** K/V của các block khớp vào `KVCache` của sequence (rồi prefill suffix). Không chia sẻ tensor (PagedAttention sẽ làm ở Phase C). Đổi lại: tốn memory bandwidth copy, nhưng tránh được compute O(prefix²) — đúng mục tiêu học tập.
- **B3 — Prefill riêng cho sequence có prefix.** Sequence khớp prefix được prefill **một mình (batch 1)** với `past` = KV prefix, `input_ids` = suffix, `position_ids = arange(matched, P)`, `attention_mask = ones(1, P)`. Sequence không khớp vẫn prefill theo batch như cũ. Lý do: các suffix dài ngắn khác nhau + prefix dài ngắn khác nhau không left-pad chung một `past` mà vẫn liền mạch token hợp lệ (tránh khoảng trống zero giữa prefix và suffix). `Qwen2Forward` đã hỗ trợ `past + Sq>1` (causal `tril(diagonal=Sk-Sq)`).
- **B4 — Insert khi sequence kết thúc.** Sau khi sequence xong, lấy `KVCache.view()` + `seq.token_ids[:cache.length]`, cắt thành block đủ và thêm vào cache (block mới chưa có). Nhờ vậy turn chat sau khớp được prefix của turn trước. Không insert khi đang chạy (tránh phức tạp).
- **B5 — LRU có biên.** Cache giới hạn `max_blocks` (mặc định ví dụ 2048 block ≈ 32K token/layer... đo trên GPU); evict LRU. Không offload/swap.
- **B6 — Cờ `AI_FACTORY_PREFIX_CACHE`** (default **on**), `AI_FACTORY_PREFIX_CACHE_BLOCKS` (default 2048), `AI_FACTORY_PREFIX_CACHE_BLOCK_SIZE` (default 16). `=0` tắt → hành vi y hệt trước.
- **B7 — Chỉ `transformers`; `LlamaBackend` nguyên vẹn; proto/Go/scheduler không đổi.**

## Global Constraints

- Test **CPU-only** với `FakeHFModel` (không cần GPU).
- **Parity là tiêu chí**: kết quả (token/stop/usage) khi bật prefix cache **giống hệt** khi tắt, cho cùng input.
- Không log prompt/raw key; không thêm dependency.
- TDD: test fail trước → implement → pass. `python -m pytest tests/`.
- Không sửa interface `EngineBackend`/`server.py`/`sampling.py`/`prompt.py`.
- Commit style: `feat(inference): ...`, `test(inference): ...`, `docs(spec): ...`.

---

## Task 1: `Qwen2Forward` chunked prefill (past + Sq>1) — nền tảng

**Files:** `python-worker/tests/test_forward.py` (bổ sung), `worker/model/attention.py`/`forward.py` (chỉ sửa nếu test fail).

- [x] Test: prefill nửa đầu prompt → "prefill" nửa sau với `past` (Sq>1, `position_ids` offset) → logits khớp **full forward** (tiny Qwen2).
- [x] Test: `attention_mask` có pad + past (như `build_decode` pad trái) cho Sq>1 khớp per-sequence (dùng `KVCacheManager`).
- [x] Xác nhận `_additive_mask` với `diagonal=Sk-Sq` đúng cho `past_len>0`; sửa nếu cần (RoPE `position_ids` offset).
- [x] `python -m pytest tests/test_forward.py` xanh.

## Task 2: `PrefixCache` (thuần, CPU)

**Files:** `python-worker/worker/prefix_cache.py` (new), `python-worker/tests/test_prefix_cache.py` (new).

- [x] Test `match(prompt_ids)`:
  - Cache rỗng → `(0, None)`.
  - Insert 2 block (32 token) rồi `match` prompt cùng prefix → `matched_len == 32`, trả K/V đúng.
  - Khớp một phần (block 2 khác token) → dừng ở block 1; block lẻ (không đủ) không cache.
  - Prompt ngắn hơn block → `0`.
- [x] Test `insert(token_ids, kv, length)`: chỉ thêm block **đủ** và **chưa có**; hash theo parent (đổi block trước → block sau không dùng lại khoá cũ).
- [x] Test LRU: vượt `max_blocks` → evict block ít dùng nhất; `get`/`match` cập nhật recency.
- [x] Implement `PrefixCache(block_size=16, max_blocks=...)`: `match`, `insert`, `clear`; nội bộ `dict[int, list[(k,v)]]` + `OrderedDict` LRU; hash ổn định (không dùng `hash()` built-in vì PYTHONHASHSEED) — dùng `hashlib.blake2b` trên bytes token ids.
- [x] `python -m pytest tests/test_prefix_cache.py` xanh.

## Task 3: Tích hợp `ContinuousBatchEngine`

**Files:** `python-worker/worker/continuous_batch_engine.py`, `python-worker/worker/engines/transformers.py` (cờ), `tests/test_prefix_engine.py` (new).

- [x] `Sequence` thêm `token_ids: list[int]`, `prefix_len: int`, `prefix_kv` (hoặc tách bước match trong `_prefill`).
- [x] `_make_sequence`: nếu bật cache → `match(prompt_ids)` lưu `prefix_len`/`prefix_kv`.
- [x] `_prefill`: tách runnable thành có/không prefix:
  - Không prefix: `self.kv.build_prefill(...)` như cũ (1 forward batch).
  - Có prefix: từng seq một — dựng `input_ids`/`attention_mask`/`position_ids`/`past` (B3), gọi forward, `cache.init_from_prefill(out.past_key_values, P, 0)`.
- [x] `_emit_or_finish`: append `tid` (token thật) vào `seq.token_ids`.
- [x] `_finish`: trước khi `cache.free()`, nếu bật cache → `insert(seq.token_ids[: cache.length], cache.view(), cache.length)`.
- [x] Cờ trong `transformers.py`: tạo `PrefixCache` khi `AI_FACTORY_PREFIX_CACHE` on, truyền vào engine.
- [x] Test engine (FakeHFModel):
  - **Parity**: cùng input, bật vs tắt cache → cùng token/final (spy forward đếm).
  - **Hit**: request A xong → request B cùng prompt → B prefill với `input_ids` ngắn hơn (spy) + output y hệt.
  - **Miss + partial**: prefix chung một phần → chỉ prefill suffix.
  - Tắt cache → không có prefix, `input_ids` = full prompt.
- [x] `python -m pytest tests/` xanh (không hồi quy).

## Task 4: Verify GPU + benchmark + docs

- [x] GPU: chat 2 turn liên tiếp trên Qwen2.5-Coder-7B 4-bit → TTFT turn 2 giảm; output không đổi so với tắt cache.
- [x] Benchmark: TTFT turn 2 (cache on/off), RAM/VRAM cache với `max_blocks` khác nhau; điều chỉnh default.
- [x] Cập nhật `CLAUDE.md`, `docs/TRACKING.md` (Phase B ✅), `docs/LEARNING_ROADMAP.md` (B5), `docs/ARCHITECTURE.md` §8.
- [ ] Commit: `feat(inference): prefix caching (week 9+ phase B)`.

---

## Self-Review Checkpoints

- [x] Bật/tắt prefix cache cho **cùng output** (token, stop reason, usage) cho mọi test.
- [x] Longest-prefix match đúng; block lẻ/không đủ không cache; hash theo parent.
- [x] LRU bounded, không rò tensor.
- [x] Sequence có prefix prefill `input_ids` ngắn hơn full prompt (chứng minh compute tiết kiệm).
- [x] `LlamaBackend`/proto/`server.py` không đổi; không thêm dependency.
- [x] `python -m pytest tests/` xanh.

## Definition of Done

Prefix cache hoạt động trên engine `transformers`: request/cùng hội thoại tái dùng KV prefix (prefill ngắn hơn), output parity khi bật/tắt, LRU bounded, cờ rollback; verify GPU có số TTFT; docs + roadmap cập nhật.
