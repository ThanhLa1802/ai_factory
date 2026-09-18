# Design — Tuần 9+: Forward pass tự viết → Prefix caching → PagedAttention

- **Ngày**: 2026-09-18
- **Trạng thái**: Draft — chờ user duyệt spec trước khi lập plan chi tiết từng phase
- **Phạm vi**: Data plane (Python worker). Tuần 7–8 đã tự quản **storage/assembly/eviction** của KV cache nhưng K/V vẫn do HF tính và attention vẫn do HF chạy. Giai đoạn này **tự viết forward pass** (attention + RoPE + layer loop), rồi **prefix caching** và **PagedAttention**. Chỉ engine `transformers` (Qwen2.5-Coder-7B); `LlamaBackend` giữ nguyên. Proto gRPC và Go **không đổi**.
- **Liên hệ**: nối tiếp spec `2026-09-14-kv-cache-continuous-batching-design.md` (D1 "để Tuần 9+"); map roadmap `docs/LEARNING_ROADMAP.md` Track B giai đoạn B5; tiến độ `docs/TRACKING.md` giai đoạn 5.

---

## 1. Bối cảnh & hiện trạng đã xác minh (từ code)

### 1.1 Forward pass hiện vẫn do HF chạy

- `ContinuousBatchEngine._prefill` (`continuous_batch_engine.py:220`) gọi
  `self.model(input_ids=..., attention_mask=..., position_ids=..., past_key_values=None, use_cache=True)`;
  `_decode_step` (`continuous_batch_engine.py:243`) gọi tương tự với `past_key_values=si.past_key_values`.
  HF `Qwen2Model.forward` tự làm **mọi thứ**: QKV projection, RoPE, causal mask, GQA, attention, MLP, và append KV.
- `worker/kv_cache.py` mới chỉ sở hữu **storage + assembly + eviction**: `KVCache` giữ tuple per-layer `[1, H_kv, S, D]`, `KVCacheManager.build_prefill/build_decode` pad trái + `position_ids` tường minh. K/V **mới** vẫn lấy từ `out.past_key_values` do HF trả.
- `TransformersBackend._get_batch()` (`engines/transformers.py:44`) truyền thẳng `self.engine.model` (HF model) vào `ContinuousBatchEngine`.
- `InferenceEngine.load()` (`engine.py:75`) load `AutoModelForCausalLM` 4-bit NF4, `device_map="auto"`, `eval()`.

### 1.2 Model & kiến trúc (đã xác minh từ `config.json`)

`Qwen/Qwen2.5-Coder-7B-Instruct`, `Qwen2ForCausalLM`:

| Thông số | Giá trị |
|---|---|
| `hidden_size` | 3584 |
| `num_hidden_layers` | 28 |
| `num_attention_heads` | 28 |
| `num_key_value_heads` | 4 (**GQA ×7**) |
| `head_dim` | 128 (= 3584/28) |
| `intermediate_size` | 18944 (SwiGLU: `silu`) |
| `rms_norm_eps` | 1e-6 |
| `rope_theta` | 1e6 (RoPE `rotate_half` kiểu GPT-NeoX) |
| `vocab_size` | 152064 |
| `use_sliding_window` | `false` → **causal mask thường** (không sliding) |

QKV projection **có bias**; `o_proj`/MLP không bias (Qwen2). Weights 4-bit NF4 (bitsandbytes) — `q/k/v/o_proj`, `gate/up/down_proj` là `bnb.nn.Linear4bit`, embedding/norm/lm_head nhỏ và thường vẫn bf16.

### 1.3 Phần đã tự viết (tái sử dụng, không đụng)

- Tokenizer byte-level BPE tự viết (`worker/model/tokenizer/`).
- Sampling loop tự viết (`worker/sampling.py`) — `sample_next_batch` chạy trên logits `[B, V]`.
- Continuous scheduler + KV storage/assembly (`worker/continuous_batch_engine.py`, `worker/kv_cache.py`).
- Chat template vẫn dùng HF `apply_chat_template` (quyết định D1 của spec tokenizer).

---

## 2. Quyết định đã chốt (brainstorming)

| # | Quyết định |
|---|---|
| **D1** | **"Forward pass tự viết" = tự viết attention + layer loop**, không dequant weights. Tái dùng các **leaf module** của HF: `model.model.embed_tokens`, `layers[i].self_attn.{q,k,v,o}_proj`, `layers[i].mlp`, `layers[i].input_layernorm`/`post_attention_layernorm`, `model.model.norm`, `model.lm_head`. Ta tự làm: RoPE, GQA expansion, causal/padding mask, softmax, concat KV, layer loop. Lý do: weights 4-bit NF4 (dequant từng weight là hướng khác, nặng hơn nhiều), và điều cần học là **cơ chế attention/KV**, không phải bitsandbytes. |
| **D2** | **Giữ interface callable tương thích HF**: đối tượng forward `__call__(input_ids, attention_mask, position_ids, past_key_values, use_cache) -> object(.logits, .past_key_values)` (legacy tuple). Nhờ vậy `ContinuousBatchEngine` **không đổi**, và test `FakeHFModel` (`tests/fake_model.py`) tiếp tục dùng nguyên. |
| **D3** | **Cache layout giữ nguyên**: legacy tuple per-layer `(k, v)` shape `[B, H_kv, S, D]` với `H_kv = 4` (GQA **không** expand trong cache; expand khi tính attention). Tương thích `KVCache.init_from_prefill`/`append_from_output` hiện tại. |
| **D4** | **RoPE tự viết** (inv_freq + cos/sin cache + `rotate_half`), nhưng **test parity** với HF (`Qwen2RotaryEmbedding`/logits). `position_ids` tường minh (kế thừa D5 tuần 7–8). |
| **D5** | **Chỉ engine `transformers`**. `LlamaBackend` (llama.cpp) nguyên vẹn. |
| **D6** | **Proto, Go, `server.py`, scheduler không đổi**. Giai đoạn này thuần Python + test. |
| **D7** | **Tự-hosts an toàn**: thêm cờ `AI_FACTORY_SELF_FORWARD` (default **on** sau khi parity xanh; `0` = quay về HF). Cho phép rollback nhanh và A/B benchmark chính xác. |
| **D8** | **Prefix caching làm trước PagedAttention** (theo đúng thứ tự `docs/TRACKING.md`), nhưng Phase B sẽ đưa vào một **block/refcount layer tối thiểu** (chunk theo block + hash + refcount) mà Phase C (PagedAttention) sẽ tổng quát hoá thành `BlockManager` đầy đủ. Nếu khi lập plan B thấy làm ngược lại (paged trước) rẻ hơn, chốt lại ở plan đó. |

---

## 3. Kiến trúc

```
ContinuousBatchEngine (KHÔNG đổi — vẫn gọi forward_fn(...) như gọi HF model)
   │
   ▼
forward_fn  (duck-typed HF interface: input_ids, attention_mask, position_ids,
             past_key_values, use_cache → .logits + .past_key_values)
   ├── Qwen2Forward   [MỚI]  — layer loop tự viết, dùng leaf module của HF
   │      ├── RotaryEmbedding  [MỚI]  — inv_freq + cos/sin + rotate_half
   │      ├── gqa_attention     [MỚI]  — repeat_kv + mask + softmax + o_proj
   │      └── embed_tokens / norms / qkvo_proj / mlp / lm_head  (HF leaf modules)
   └── FakeHFModel (tests)  — giữ nguyên
```

Bất biến: **chỉ scheduler thread gọi forward**; `KVCache`/`KVCacheManager` vẫn là nơi duy nhất sở hữu storage/assembly; forward chỉ tính toán và trả KV tuple.

---

## 4. Phase A — Forward pass tự viết (phase được lập plan chi tiết trước)

### 4.1 Module mới

**`python-worker/worker/model/rope.py`** — RoPE thuần:

```python
class RotaryEmbedding:
    def __init__(self, head_dim, theta=1e6, max_len=..., device=None, dtype=...): ...
    def cos_sin(self, position_ids):  # [B,S] -> ([B,S,D//2...], ...) hoặc [1,S,D]
    def apply(self, q, k, position_ids):  # rotate_half, trả q_embed, k_embed
```

**`python-worker/worker/model/attention.py`** — attention GQA thuần:

```python
def repeat_kv(x, n_rep): ...                 # [B,Hkv,S,D] -> [B,Hkv*n_rep,S,D]
def gqa_attention(q, k, v, attention_mask=None, causal=False):
    # q [B,Hq,Sq,D], k/v [B,Hkv,Sk,D]; scale 1/sqrt(D)
    # mask: key-padding (attention_mask [B,Sk]) + causal tril khi Sq>1
    # softmax fp32 -> cast lại dtype -> @ v -> [B,Hq,Sq,D]
```

**`python-worker/worker/model/forward.py`** — `Qwen2Forward`:

```python
class Qwen2Forward:
    def __init__(self, hf_model):   # đọc config + giữ ref leaf modules
    def __call__(self, input_ids, attention_mask=None, position_ids=None,
                 past_key_values=None, use_cache=True, **kw) -> ForwardOutput:
        # h = embed_tokens(input_ids)
        # for layer in layers:
        #     x = layer.input_layernorm(h)
        #     q,k,v = layer.self_attn.q_proj(x), k_proj(x), v_proj(x)
        #     q,k = rope.apply(q.reshape(...), k.reshape(...), position_ids)
        #     k_all, v_all = concat(past_k, k), concat(past_v, v)
        #     a = gqa_attention(q, k_all, v_all, attention_mask, causal=Sq>1)
        #     h = h + layer.self_attn.o_proj(a.reshape(B,Sq,-1))
        #     h = h + layer.mlp(layer.post_attention_layernorm(h))
        # h = model.norm(h); logits = lm_head(h)
        # return ForwardOutput(logits, tuple((k_all,v_all) per layer))
```

`ForwardOutput` có `.logits` + `.past_key_values` (giống `FakeOutput`/HF output).

### 4.2 Chi tiết attention

- `q`: reshape `[B,Sq,Hq,D]` → transpose → `[B,Hq,Sq,D]`; `k`,`v`: `[B,Hkv,Sq,D]`.
- RoPE áp trên `q` và `k_cur` **trước** khi concat cache, dùng `position_ids` `[B,Sq]`.
- `k_all = cat(past_k, k_cur, dim=2)`, tương tự `v_all`; `Sk = past_len + Sq`.
- `repeat_kv(k_all, Hq//Hkv)` → `[B,Hq,Sk,D]`.
- `scores = q @ k_allᵀ / sqrt(D)` shape `[B,Hq,Sq,Sk]`.
- Mask: key-padding từ `attention_mask` `[B,Sk]` → broadcast `[B,1,1,Sk]`; **causal** khi `Sq>1` (prefill): `tril(diagonal=Sk-Sq)`. Decode (`Sq==1`) không cần causal (mọi key quá khứ hợp lệ, pad đã bị mask). Không có chunked prefill nên trường hợp `past_len>0 & Sq>1` không xảy ra (D6 kế thừa).
- Softmax theo chiều `Sk` (tính ở fp32 rồi cast lại dtype), `@ v_all` → `[B,Hq,Sq,D]`.

### 4.3 Tích hợp

- `TransformersBackend._get_batch()`: `ContinuousBatchEngine(Qwen2Forward(self.engine.model), ...)` khi `AI_FACTORY_SELF_FORWARD` on; ngược lại truyền `self.engine.model` như cũ.
- Không sửa `ContinuousBatchEngine`, `kv_cache.py`, `sampling.py`, `server.py`.

### 4.4 Kiểm thử (TDD, CPU-only)

1. **Tiny Qwen2 harness**: dựng `Qwen2ForCausalLM` nhỏ (ví dụ hidden 64, 2 layer, 8 head / 2 kv head, vocab 128, theta nhỏ) random weights trên CPU → so logits self-forward vs HF forward (`torch.allclose`, `atol/rtol` hợp lý).
2. **RoPE parity**: cos/sin + `apply` khớp HF `Qwen2RotaryEmbedding` / hàm `apply_rotary_pos_emb` cho cùng `position_ids`.
3. **Attention**: shape đúng; causal đúng (token không nhìn thấy tương lai); padding mask đúng; GQA expand đúng (Hq=Hkv×rep).
4. **Full forward parity**: prompt đầy đủ (không cache) khớp HF; **incremental decode** dùng cache self-written khớp HF (prefill rồi từng bước `Sq=1`, so logits từng bước).
5. **Tích hợp**: `python -m pytest tests/` xanh (bao gồm `test_continuous_batch.py`, `test_kv_cache.py` với `FakeHFModel` — chứng minh interface D2 không vỡ).
6. **GPU verify (thủ công, ngoài CI)**: trên Qwen2.5-Coder-7B 4-bit thật — so logits/self-output với HF (`max abs diff`), chạy E2E chat SSE, đo TTFT/throughput, xác nhận không OOM (12GB).

---

## 5. Phase B — Prefix caching (thiết kế sơ, plan riêng sau)

**Mục tiêu**: tái dùng KV của **prefix chung** giữa các request (system prompt, các block prompt giống nhau) để bỏ prefill lặp → giảm TTFT.

- **Block hoá**: tokenize prompt → chia thành block `BLOCK_SIZE` token (ví dụ 16). Block chỉ được cache khi **đủ** (block cuối lẻ không cache).
- **Radix/hash matching**: `hash_i = H(parent_hash, token_ids_of_block_i)`; cây prefix map `hash → KV block vật lý (refcount)`.
- **Prefill**: duyệt cây tìm **longest prefix match**; adopt các block khớp (refcount++), chỉ prefill **phần suffix** chưa khớp; nối K/V block + K/V suffix.
- **Vòng đời**: sequence xong → refcount-- ; block refcount==0 giữ lại trong cache (LRU evict khi đầy), có thể tái dùng cho request sau.
- **Điều kiện đúng**: chỉ cache block có token ids **ổn định** (đã áp chat template, không phụ thuộc request khác); cẩn thận với tool definitions/system prompt khác nhau → khoá hash bao gồm cả token ids nên vẫn đúng, chỉ giảm hit rate.
- **Non-goal B**: chưa cần swap/offload; chưa CoW phức tạp.

## 6. Phase C — PagedAttention (thiết kế sơ, plan riêng sau)

**Mục tiêu**: thay buffer per-sequence liền mạch bằng **block vật lý cố định** + **block table** per-sequence → gần như không phân mảnh, hỗ trợ chia sẻ CoW (nền cho prefix cache) và mở đường swap/offload.

- **`BlockManager`**: pool block `[num_blocks, H_kv, BLOCK_SIZE, D]` × `num_layers` (theo layer); `allocate()/free()/fork()`; LRU khi hết.
- **`BlockTable`**: mỗi sequence giữ list block id + `num_tokens`; block cuối có thể chưa đầy.
- **Attention**: gather K/V theo block table (PyTorch gather, **không viết CUDA kernel** — nêu rõ giới hạn học thuật) rồi chạy cùng `gqa_attention`.
- **Copy-on-Write**: khi ghi vào block đang share (refcount>1) thì copy block trước khi ghi — cầu nối hoàn hảo với Phase B.
- **Thay thế dần**: `KVCache`/`KVCacheManager` hiện tại được thay bằng `BlockManager`/`BlockTable`; parity test với Phase A (cùng output greedy).
- **Ghi chú đánh đổi**: bản PyTorch gather sẽ **không** nhanh bằng kernel CUDA của vLLM; mục tiêu là học cơ chế quản lý bộ nhớ + đo lường, không phải tối ưu tuyệt đối.

---

## 7. Testing tổng

- **CPU-only** cho mọi test logic (tiny model / fake model). Không cần GPU trong CI.
- **Parity là tiêu chí số 1**: self-forward phải khớp HF trên tiny model (`allclose`), và khi chạy GPU phải khớp/近 trên model 4-bit (sai số chấp nhận do quantize).
- `python -m pytest tests/` phải xanh trước và sau mỗi phase.
- Benchmark: `python-worker/benchmark.py` (hoặc thêm chế độ) đo TTFT/throughput self vs HF, và hiệu quả prefix caching (tỉ lệ hit, TTFT giảm).

## 8. Risks / open questions

- **RoPE / mask sai** → output lệch nhưng vẫn "chạy"; bắt buộc parity test + so `max abs diff` trên GPU.
- **4-bit + leaf module**: gọi `bnb.nn.Linear4bit` trực tiếp ổn, nhưng dtype/`device_map` (một phần layer trên CPU) có thể gây lệch device — cần `input_ids`/`position_ids` đúng device như hiện tại.
- **`attention_mask` dtype/none**: scheduler luôn truyền mask cho decode; prefill mask `[B,P]`. Forward phải chịu được `attention_mask=None` (test thuần) và `position_ids=None` (tự suy ra `arange`).
- **Hiệu năng**: attention tự viết bằng PyTorch có thể **chậm hơn** SDPA của HF; nếu chậm, cân nhắc gọi `F.scaled_dot_product_attention` cho phần tự viết (vẫn là "tự viết" ở mức kiểm soát KV/mask) — ghi nhận trade-off, đo trước khi quyết.
- **B/C ordering**: prefix caching cần block/refcount — có thể trùng với PagedAttention; chốt ở plan Phase B (D8).
- **VRAM 12GB**: block pool của PagedAttention cần ngân sách rõ; đo trên máy thật.

## 9. Files touched (dự kiến)

**Phase A**
- `python-worker/worker/model/rope.py` — MỚI.
- `python-worker/worker/model/attention.py` — MỚI.
- `python-worker/worker/model/forward.py` — MỚI.
- `python-worker/worker/model/hf_forward.py` — MỚI (adapter rollback: legacy tuple ↔ HF `Cache`).
- `python-worker/worker/engines/transformers.py` — tích hợp + cờ `AI_FACTORY_SELF_FORWARD`.
- `python-worker/tests/test_rope.py`, `tests/test_self_attention.py`, `tests/test_forward.py` — MỚI.
- Docs: `CLAUDE.md`, `docs/TRACKING.md`, `docs/LEARNING_ROADMAP.md`, `docs/ARCHITECTURE.md` (§8) — cập nhật khi phase xong.

**Phase B / C** — xác định ở plan riêng (dự kiến `worker/kv_cache.py` tiến hoá thành `block_manager.py` + `prefix_cache.py`).

## 10. Non-goals (toàn giai đoạn)

- ❌ Không dequant/không viết lại Linear/Embedding (D1).
- ❌ Không CUDA kernel (PagedAttention bản PyTorch gather).
- ❌ Không speculative decoding, không chunked prefill (kế thừa D6).
- ❌ Không đổi proto/Go/`LlamaBackend`/scheduler.
- ❌ Không multi-GPU/tensor parallel.
