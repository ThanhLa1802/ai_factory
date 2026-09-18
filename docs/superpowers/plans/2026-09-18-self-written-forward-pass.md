# Self-written Forward Pass — Implementation Plan (Tuần 9+, Phase A)

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** [`docs/superpowers/specs/2026-09-18-self-written-forward-pass-design.md`](../specs/2026-09-18-self-written-forward-pass-design.md) (D1–D8).

**Goal:** Thay forward pass của HF bằng forward pass tự viết (attention GQA + RoPE + causal/padding mask + layer loop) cho engine `transformers`, **tái dùng leaf module** của HF (weights 4-bit NF4). Chỉ đổi data plane Python; proto/Go/scheduler/`LlamaBackend` không đổi.

**Scope:** `python-worker/worker/model/{rope,attention,forward}.py` (mới) + `worker/engines/transformers.py` (tích hợp) + `python-worker/tests/**`. Không prefix caching, không PagedAttention (phase B/C), không dequant, không CUDA kernel.

**Tech:** Python 3.12 + torch + transformers (không thêm dependency).

---

## Decisions (locked)

- **D1** — Tự viết attention + layer loop; tái dùng leaf module HF (`embed_tokens`, `layers[i].self_attn.{q,k,v,o}_proj`, `layers[i].mlp`, norms, `model.norm`, `lm_head`). Không dequant.
- **D2** — Forward callable **tương thích interface HF**: `__call__(input_ids, attention_mask, position_ids, past_key_values, use_cache) -> .logits + .past_key_values` (legacy tuple). `ContinuousBatchEngine` + `FakeHFModel` không đổi.
- **D3** — Cache legacy tuple `(k, v)` per-layer `[B, H_kv, S, D]`, GQA giữ 4 head (không expand trong cache).
- **D4** — RoPE tự viết, parity test với HF; `position_ids` tường minh.
- **D5** — Chỉ `transformers`.
- **D6** — Không đổi proto/Go/`server.py`/scheduler.
- **D7** — Cờ `AI_FACTORY_SELF_FORWARD` (default on sau parity; `0` → dùng HF).
- **D8** — Prefix caching/PagedAttention để phase sau.

## Global Constraints

- Test **CPU-only**, không cần GPU trong CI (dùng tiny `Qwen2ForCausalLM` random weights).
- **Parity là tiêu chí chính**: self-forward vs HF forward phải `allclose` trên tiny model; GPU verify thủ công so `max abs diff`.
- Không log prompt/raw key; không thêm dependency.
- TDD: test fail trước → implement → pass. Chạy `python -m pytest tests/`.
- `ContinuousBatchEngine`, `kv_cache.py`, `sampling.py`, `server.py`, `FakeHFModel` **không sửa** (chứng minh D2).
- Commit style: `feat(inference): ...`, `test(inference): ...`, `docs(spec): ...`.
- Verify cuối: `cd python-worker && python -m pytest tests/`.

---

## Task 1: Tiny Qwen2 test harness (parity baseline)

**Files:** `python-worker/tests/tiny_qwen.py` (new), `python-worker/tests/test_forward.py` (new — baseline).

- [x] Thêm factory `build_tiny_qwen()` dùng `Qwen2Config` nhỏ (ví dụ `hidden_size=64`, `num_hidden_layers=2`, `num_attention_heads=8`, `num_key_value_heads=2`, `intermediate_size=128`, `vocab_size=128`, `rope_theta=10000.0`, `max_position_embeddings=256`), `Qwen2ForCausalLM(config).eval()`, seed cố định để tái lập.
- [x] Test baseline: HF `model(input_ids).logits` ổn định/chạy được; shape `[B, S, vocab]`.
- [x] Test baseline: HF prefill + incremental decode (`past_key_values`) cho ra logits bước cuối khớp full forward (xác nhận harness dùng đúng).
- [x] `python -m pytest tests/test_forward.py` xanh.

## Task 2: RoPE tự viết + parity

**Files:** `python-worker/worker/model/rope.py` (new), `python-worker/tests/test_rope.py` (new).

- [x] Test: `RotaryEmbedding(head_dim, theta)` — `cos/sin` shape đúng; giá trị khớp HF `Qwen2RotaryEmbedding` cho cùng `position_ids`.
- [x] Test: `apply(q, k, position_ids)` khớp HF `apply_rotary_pos_emb` (rotate_half kiểu GPT-NeoX), cả `Sq>1` (prefill) và `Sq=1` (decode).
- [x] Test: `position_ids` không liên tục (có offset, ví dụ decode vị trí `L`) cho RoPE đúng — bảo vệ chỗ dễ sai với `past`.
- [x] Implement `RotaryEmbedding`: `inv_freq = 1/theta**(arange(0,D,2)/D)`; `cos/sin` cache theo `max_len`; `apply` dùng `rotate_half`.
- [x] `python -m pytest tests/test_rope.py` xanh.

## Task 3: GQA attention tự viết

**Files:** `python-worker/worker/model/attention.py` (new), `python-worker/tests/test_self_attention.py` (new).

- [x] Test `repeat_kv`: `[B,Hkv,S,D] -> [B,Hkv*rep,S,D]`, giá trị lặp đúng thứ tự.
- [x] Test shape: `gqa_attention(q,k,v)` trả `[B,Hq,Sq,D]`; với `Hq==Hkv` (MHA) khớp công thức tham chiếu.
- [x] Test causal: token không attend tương lai (thay đổi k/v tương lai không đổi output quá khứ).
- [x] Test padding mask: key bị mask (attention_mask=0) không ảnh hưởng output (so với việc bỏ hẳn key đó).
- [x] Test decode (`Sq=1`): attend toàn bộ key hợp lệ.
- [x] Implement `gqa_attention(q, k, v, attention_mask=None, causal=False)` — scale `1/sqrt(D)`, additive mask, softmax fp32 → cast lại, `@ v`.
- [x] `python -m pytest tests/test_self_attention.py` xanh.

## Task 4: `Qwen2Forward` + parity vs HF

**Files:** `python-worker/worker/model/forward.py` (new), `python-worker/tests/test_forward.py` (bổ sung).

- [x] Test: full prompt (không cache) → `logits` khớp HF `allclose` (tiny model, `torch.no_grad()`).
- [x] Test: **incremental** — prefill bằng `Qwen2Forward`, rồi decode từng bước với cache của self-forward (raw tuple) → logits mỗi bước khớp HF chạy cùng cách.
- [x] Test: forward chịu được `attention_mask=None`, `position_ids=None` (tự suy `arange`).
- [x] Test: `past_key_values` trả về đúng shape `[B, Hkv, S_total, D]` mỗi layer, GQA `Hkv=2` (không expand).
- [x] Test: gọi với `past_key_values` đã pad trái (`KVCacheManager.build_decode` output) → slot cuối là token mới (tương thích `KVCache.append_from_output`).
- [x] Implement `Qwen2Forward` (D1/D2): layer loop, dùng leaf modules HF, áp RoPE trên q/k_cur, concat past, attention, resid + MLP, final norm + lm_head; trả object `.logits` + `.past_key_values`.
- [x] `python -m pytest tests/test_forward.py` xanh.

## Task 5: Tích hợp `TransformersBackend`

**Files:** `python-worker/worker/engines/transformers.py`, `python-worker/tests/test_engines.py` (cập nhật nếu cần).

- [x] Thêm cờ `AI_FACTORY_SELF_FORWARD` (default on; `0`/`false`/`off` → HF).
- [x] `_get_batch()` truyền `Qwen2Forward(self.engine.model)` khi bật, ngược lại `self.engine.model`.
- [x] Test: với forward giả, cả hai nhánh tạo engine và trả `(request_id, event)` đúng contract.
- [x] `python -m pytest tests/` xanh — **không hồi quy** `test_continuous_batch.py`, `test_kv_cache.py`, `test_engines.py`.

## Task 6: GPU verify + benchmark + docs

- [x] GPU (ngoài CI): chạy `python -m worker.server`, E2E chat; so `max abs diff` logits self vs HF trên Qwen2.5-Coder-7B 4-bit (sai số nhỏ chấp nhận do quantize); xác nhận không OOM 12GB.
- [x] Benchmark `python-worker/benchmark.py` (hoặc thêm chế độ): TTFT + throughput self vs HF (batch 1 và batch 4); ghi lại số.
- [x] Nếu self-forward chậm hơn đáng kể: thử `F.scaled_dot_product_attention` cho phần tính attention (giữ tự viết mask/KV), đo lại — ghi trade-off vào spec/ARCHITECTURE.
- [x] Cập nhật `CLAUDE.md` (Engineering decisions / roadmap), `docs/TRACKING.md` (giai đoạn 5 tiến độ), `docs/LEARNING_ROADMAP.md` (B5), `docs/ARCHITECTURE.md` §8 — mô tả forward tự viết.
- [ ] Commit theo cụm: rope/attention → forward → tích hợp → docs.

---

## Self-Review Checkpoints

- [x] `Qwen2Forward` khớp HF trên tiny model (full + incremental) với `allclose`.
- [x] RoPE với `position_ids` offset (decode) đúng — không lệch so HF.
- [x] GQA cache giữ 4 head, expand đúng lúc attention.
- [x] `ContinuousBatchEngine`/`kv_cache.py`/`FakeHFModel` **không sửa** — interface D2 chứng minh bằng test xanh.
- [x] Cờ `AI_FACTORY_SELF_FORWARD` rollback được về HF.
- [x] `python -m pytest tests/` xanh; không thêm dependency.
- [x] GPU verify: forward parity (bf16/eager bit-exact), có số prefill/decode self vs HF.

## Definition of Done

`TransformersBackend` mặc định chạy forward pass tự viết (RoPE + GQA attention + layer loop, dùng leaf module HF 4-bit); parity với HF trên test CPU (full + incremental) và verify thật trên GPU; cờ rollback hoạt động; docs + roadmap cập nhật.
