# PagedAttention — Implementation Plan (Tuần 9+, Phase C)

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** [`docs/superpowers/specs/2026-09-18-self-written-forward-pass-design.md`](../specs/2026-09-18-self-written-forward-pass-design.md) §6 (D8).

**Goal:** Thay buffer per-sequence liền mạch (`KVCache`) bằng **pool block vật lý cố định** + **block table** per-sequence → gần như không phân mảnh, chia sẻ block bằng **refcount** (nền cho prefix cache thật, thay copy-on-adopt của Phase B), hỗ trợ **Copy-on-Write**. Attention gather K/V theo block table (PyTorch gather, **không CUDA kernel**) rồi chạy lại `gqa_attention`. Chỉ engine `transformers`; proto/Go/`LlamaBackend` không đổi.

**Scope:** `python-worker/worker/block_manager.py` (mới: `BlockManager` + `PagedKVCache` + `BlockPrefixCache`) + `worker/kv_cache.py` (assemble qua `view()`) + `worker/continuous_batch_engine.py` (cache factory + prefix block) + `worker/engines/transformers.py` (cờ) + tests.

**Tech:** Python 3.12 + torch (không thêm dependency).

---

## Decisions (locked)

- **C1 — Pool block vật lý cố định.** `BlockManager` giữ pool per-layer `[num_blocks, H_kv, block_size, head_dim]` (lazy alloc theo dtype/device của K/V đầu tiên). Block quản bằng `refcount`; block `refcount==0` nằm trong **free LRU** và có thể bị tái dùng; hết block → `OutOfBlocks`.
- **C2 — `BlockTable` per-sequence.** `PagedKVCache` giữ `block_table: list[int]` + `length`. Block cuối có thể chưa đầy. `view()` gather block theo table → `[1, H_kv, length, D]` per layer để assemble batch (giữ nguyên `KVCacheManager`).
- **C3 — Gather bằng PyTorch, không CUDA kernel.** `BlockManager.gather()` nối các block rồi cắt theo `length`; `gqa_attention` không đổi. Ghi nhận trade-off: chậm hơn PagedAttention kernel của vLLM; mục tiêu học cơ chế quản lý bộ nhớ.
- **C4 — Copy-on-Write.** Ghi vào block có `refcount>1` → `copy_on_write()` cấp block mới, copy dữ liệu, `decref` block cũ, cập nhật table. `PagedKVCache.fork()` chia sẻ toàn bộ block (incref) → append vào bản fork/bản gốc kích hoạt CoW ở block cuối chưa đầy.
- **C5 — Prefix share bằng block (thay copy tensor).** `BlockPrefixCache` map `blake2b(parent_hash, block_tokens) → block_id`. `match()` trả block ids đã **incref** (longest prefix, cắt `((n-1)//block_size)*block_size` để chừa ≥1 token prefill); `insert()` đăng ký hash cho các block đủ của sequence. `BlockManager` gọi `on_evict(block_id)` khi tái dùng block refcount 0 → cache xoá mapping (best-effort).
- **C6 — Cờ `AI_FACTORY_PAGED_ATTENTION`** (default **off** để tránh hồi quy hiệu năng chưa đo trên GPU), `AI_FACTORY_PAGED_BLOCKS` (default 2048), `AI_FACTORY_PAGED_BLOCK_SIZE` (default 16, khớp `AI_FACTORY_PREFIX_CACHE_BLOCK_SIZE`). `=1` bật; khi bật dùng `PagedKVCache` + `BlockPrefixCache`.
- **C7 — Chỉ `transformers`; `LlamaBackend`/proto/`server.py`/`sampling.py` nguyên vẹn.**

## Global Constraints

- Test **CPU-only** (`FakeHFModel` + tiny tensor), không cần GPU.
- **Parity là tiêu chí**: cùng input, paged on vs off → token/stop/usage **giống hệt**.
- Không thêm dependency; không log prompt/raw key.
- TDD: test fail trước → implement → pass. `python -m pytest tests/` (conda env `mywork`) phải xanh.
- Không đổi interface `EngineBackend`/`server.py`/proto.

---

## Task 1: `BlockManager` (thuần, CPU)

**Files:** `python-worker/worker/block_manager.py` (new), `python-worker/tests/test_block_manager.py` (new).

- [x] `OutOfBlocks(RuntimeError)`; `BlockManager(num_blocks, num_layers, num_kv_heads, block_size, head_dim)`.
- [x] `alloc()` → block LRU rảnh (refcount 0 → 1), gọi `on_evict(bid)`; hết → `OutOfBlocks`.
- [x] `incref/decref`: `decref` về 0 → trả vào free LRU.
- [x] `write(bid, layer, start, k, v)` ghi `[H, n, D]` vào pool; pool tạo lazy theo dtype/device.
- [x] `gather(block_table, length, layer)` → `[1, H, length, D]` (block cuối cắt theo length).
- [x] `copy_on_write(bid)` → block mới chứa bản copy, `decref` block cũ.
- [x] Test: alloc/refcount/free; gather đúng nhiều block + block lẻ; CoW tách dữ liệu; `OutOfBlocks`; `on_evict` gọi khi tái dùng.

## Task 2: `PagedKVCache` (thuần, CPU)

**Files:** `worker/block_manager.py`, `tests/test_block_manager.py`.

- [x] `init_from_prefill(past, prompt_len, row=0)`: ghi các vị trí `[length, prompt_len)` (hỗ trợ sẵn prefix đã adopt); `append_from_output(past, row)`: ghi slot cuối.
- [x] `adopt(block_ids)`: incref + set table + `length += len(ids)*block_size`; `fork()`; `view()`; `free()`; `capacity`.
- [x] CoW khi ghi vào block `refcount>1`; `_write_at` dùng chung cho prefill/append.
- [x] Test: init/append khớp `KVCache` (cùng giá trị `view()`); block boundary đúng; adopt không copy dữ liệu (`view()` = dữ liệu gốc); fork + append kích hoạt CoW, hai cache độc lập; `free()` trả hết block (refcount 0).

## Task 3: `BlockPrefixCache`

**Files:** `worker/block_manager.py`, `tests/test_block_prefix_cache.py`.

- [x] `match(token_ids)` → `(matched_len, block_ids)`, incref đúng các block, cắt `max_full = ((n-1)//bs)*bs`, LRU touch.
- [x] `insert(token_ids, cache)`: đăng ký hash các block đủ chưa có; giới hạn `max_blocks` (bỏ mapping LRU).
- [x] `on_evict` xoá mapping khi block bị tái dùng; match sau eviction → miss.
- [x] Test: hit/partial/miss; block lẻ không cache; hash theo parent (đổi block trước → block sau không hit); eviction.

## Task 4: Tích hợp engine + cờ

**Files:** `worker/kv_cache.py`, `worker/continuous_batch_engine.py`, `worker/engines/transformers.py`, `tests/test_paged_engine.py`.

- [x] `KVCacheManager.build_decode` assemble qua `cache.view()` (một lần/cache) thay vì `cache.layers[layer]`.
- [x] `ContinuousBatchEngine`: thêm `paged: bool`, `block_manager`, `cache_factory`; `Sequence.prefix_blocks`; `_make_sequence`/`_match_prefix` nhánh paged; `_prefill` adopt + gather prefix; `_finish` insert block.
- [x] `TransformersBackend`: `AI_FACTORY_PAGED_ATTENTION` → build `BlockManager` từ config model + `BlockPrefixCache` + `cache_factory`.
- [x] Test engine (FakeHFModel):
  - **Parity** paged on/off (token/stop/usage).
  - **Block share**: request A xong → B cùng prompt chỉ prefill suffix (spy forward); chia sẻ block/refcount/CoW kiểm ở `test_block_manager`. 
  - **Multi-sequence decode** batch nhiều độ dài (block_size nhỏ → nhiều block/seq).
- [x] `python -m pytest tests/` xanh (không hồi quy).

## Task 5: Docs + roadmap

- [x] Cập nhật `CLAUDE.md`, `docs/TRACKING.md` (Phase C ✅), `docs/LEARNING_ROADMAP.md` (B5), `docs/ARCHITECTURE.md` §8.
- [ ] Commit: `feat(inference): paged attention (week 9+ phase C)`.

---

## Self-Review Checkpoints

- [x] Parity paged on/off cho mọi test (token, stop reason, usage).
- [x] Block chia sẻ thật (refcount), không copy prefix; CoW tách dữ liệu khi chia sẻ.
- [x] Free trả hết block; không rò block (refcount luôn về 0 sau `free`).
- [x] `LlamaBackend`/proto/`server.py` không đổi; không thêm dependency.
- [x] `python -m pytest tests/` xanh.

## Definition of Done

PagedAttention chạy trên engine `transformers`: KV lưu trong block pool cố định + block table per-sequence, gather theo table, CoW, prefix chia sẻ block thật; parity khi bật/tắt; cờ rollback; docs + roadmap cập nhật.
