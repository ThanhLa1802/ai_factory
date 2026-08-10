# AI Factory — Benchmark Report

**Date:** 2026-08-01
**Model:** Qwen/Qwen2.5-3B-Instruct (4-bit NF4)
**GPU:** NVIDIA GeForce RTX 3060 12GB VRAM
**Framework:** HuggingFace Transformers + bitsandbytes

---

## 1. KV Cache Memory Model

### Cơ chế

Mỗi token được sinh ra, model lưu **Key (K)** và **Value (V)** cho tất cả các layer vào VRAM. Các token sau dùng KV cache này để tính attention — không cần tính lại từ đầu → giảm compute từ O(n²) xuống O(n) trong pha decode.

```
Attention(Q_mới, [K₁,K₂,...,K_n], [V₁,V₂,...,V_n]) → token tiếp theo
```

Tuy nhiên attention vẫn phải nhìn toàn bộ K,V cũ → **độ phức tạp vẫn là O(n²)** nhưng phần lớn là memory-bound thay vì compute-bound.

### Qwen 2.5 3B Architecture

| Tham số | Giá trị |
|---------|---------|
| Layers | 36 |
| Hidden size | 2048 |
| Attention heads (Q) | 16 |
| KV heads | 2 (GQA 16:2) |
| Head dim | 128 |
| Max position | 32,768 |

### Grouped Query Attention (GQA)

```
Q: 16 heads × 128 dim = mỗi head nhìn toàn bộ context
K:  2 heads × 128 dim = shared giữa 8 Q-heads
V:  2 heads × 128 dim = shared giữa 8 Q-heads

→ KV cache giảm 8× so với MHA (multi-head attention)!
```

### Công thức KV Cache

```
KV_per_token = Layers × 2(K+V) × KV_heads × Head_dim × Bytes_per_element
             = 36 × 2 × 2 × 128 × 2 bytes (bfloat16)
             = 36,864 bytes
             = 0.037 MB / token
             = 37 MB / 1000 tokens
```

### Memory Scaling

| Context Length | KV Cache | + Model Weights (4-bit) | Total VRAM | % of 12.9 GB |
|:---:|:---:|:---:|:---:|:---:|
| 256 | 9 MB | 3.2 GB | 3.21 GB | 24.9% |
| 512 | 19 MB | 3.2 GB | 3.22 GB | 25.0% |
| 1,024 | 38 MB | 3.2 GB | 3.24 GB | 25.1% |
| 2,048 | 75 MB | 3.2 GB | 3.28 GB | 25.4% |
| 4,096 | 151 MB | 3.2 GB | 3.35 GB | 26.0% |
| **8,192** | **302 MB** | **3.2 GB** | **3.50 GB** | **27.1%** |
| 16,384 | 604 MB | 3.2 GB | 3.80 GB | 29.5% |
| 32,768 | 1.2 GB | 3.2 GB | 4.41 GB | 34.2% |

> **Kết luận:** VRAM không phải bottleneck. Max context lý thuyết đạt **265,850 tokens** — vượt xa giới hạn 32K của model. Bottleneck thực sự là **compute O(n²)** của attention.

---

## 2. VRAM Breakdown (Actual)

| Component | Size | % VRAM |
|-----------|------|--------|
| Model weights (4-bit) | 2.1 GB | 16.3% |
| KV Cache (8K context) | 0.3 GB | 2.3% |
| PyTorch overhead | ~0.3 GB | 2.3% |
| **Đã dùng** | **~2.7 GB** | **20.9%** |
| Còn trống cho KV cache | 9.8 GB | 76.0% |
| Safety buffer | 1.0 GB | 7.8% |

---

## 3. Latency Benchmarks

### Per-request measurements

| Scenario | Prompt Tokens | Generate Tokens | Total Time | TTFT | TPOT (avg) | TPOT (p50) | Throughput |
|----------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| Short | 6 | 50 | 4,591 ms | 85 ms | 74.9 ms | 75.7 ms | 10.9 tok/s |
| Medium | 13 | 150 | 11,412 ms | 73 ms | 79.2 ms | 77.9 ms | 13.1 tok/s |
| Long | 14 | 300 | 23,521 ms | 69 ms | 81.9 ms | 79.1 ms | 12.8 tok/s |

### Key Metrics Explained

| Metric | Ý nghĩa | Công thức |
|--------|---------|-----------|
| **TTFT** | Time to First Token — người dùng chờ bao lâu để thấy token đầu tiên | `t_first_token - t_request` |
| **TPOT** | Time Per Output Token — tốc độ gen từng token (không tính token đầu) | `(t_last - t_first) / (n - 1)` |
| **Throughput** | Tổng token/giây cho toàn bộ request | `total_tokens / total_time` |

### Prefill vs Decode

```
Request: 10 token prompt → generate 100 tokens

  ┌──────────┐       ┌──────────────────────────────┐
  │ PREFILL  │  ──→  │           DECODE              │
  │ (1 pass) │       │       (100 passes)            │
  │          │       │                               │
  │ Process  │       │ Each step:                    │
  │ all 10   │       │  1. Attention vs KV cache     │
  │ tokens   │       │  2. Generate 1 new token      │
  │ at once  │       │  3. Store new K,V in cache    │
  │  → 73ms  │       │  → 75ms × 100 = 7,500ms       │
  └──────────┘       └──────────────────────────────┘

  Total: 73ms (prefill) + 7,500ms (decode) ≈ 7.6s
```

---

## 4. Context Length vs Latency

| Prompt Tokens | Time (prefill + 10 gen) |
|:---:|:---:|
| 128 | 936 ms |
| 256 | 787 ms |
| 512 | 847 ms |
| 1,024 | 984 ms |
| 2,048 | 1,305 ms |

> Prefill tăng **gần như tuyến tính O(n)** — không phải O(n²). Nhờ **Flash Attention / SDPA** (scaled dot-product attention với memory-efficient kernel), context dài vẫn khả thi. Đây là lý do model 128K+ tokens khả dụng trong thực tế.

---

## 5. Batch Scaling

| Batch Size | Time | Total Tokens | Throughput | Efficiency |
|:---:|:---:|:---:|:---:|:---:|
| 1 | 2,139 ms | 30 | 14.0 tok/s | 1.0× (baseline) |
| 2 | 2,280 ms | 60 | 26.3 tok/s | **1.9×** |
| 4 | 2,232 ms | 120 | 53.8 tok/s | **3.8×** |

> GPU bị under-utilized ở single stream. Static batching tận dụng GPU compute tốt hơn gần 4×. Production dùng **Continuous Batching** — tự động gộp các request đến không cùng lúc, tối ưu hơn static batch.

---

## 6. Production Comparison

| Metric | AI Factory (Current) | Production (GLM 5.2 / H100) |
|--------|---------------------|---------------------------|
| **Model** | Qwen 2.5 3B (GQA 16:2) | GLM 5.2 100B+ (GQA) |
| **Quantization** | 4-bit NF4 (bitsandbytes) | FP8 native (H100 Tensor Core) |
| **Model VRAM** | 2.1 GB | 200+ GB (multi-GPU) |
| **KV / 1000 tokens** | 0.037 GB | ~1-3 GB |
| **TTFT** | 70-85 ms ✅ | <100 ms |
| **TPOT** | 75-82 ms ❌ | <10 ms |
| **Max Context** | 265K (VRAM-bound) | 128K-1M (compute-bound) |
| **Throughput (single)** | 13 tok/s ❌ | 100+ tok/s |
| **Throughput (batch)** | 54 tok/s | 1000+ tok/s |
| **Batching** | Static batch | Continuous Batching |
| **Parallelism** | None | TP + PP (24+ GPUs) |
| **KV Cache Management** | HF default | PagedAttention / prefix caching |

---

## 7. Summary: What Matters for Production

### Những thứ project này làm đúng

1. ✅ **Kiến trúc tách biệt** — Go API server + Python inference worker (giống production: API gateway + vLLM/TensorRT-LLM backend)
2. ✅ **Streaming token-level** — gRPC server-streaming + SSE (giống production)
3. ✅ **Multi-turn agentic loop** — đúng pattern tool-use của Anthropic/OpenAI
4. ✅ **Cancel propagation** — context cancellation xuyên suốt chain
5. ✅ **Dual protocol** — Anthropic + OpenAI compatibility

### Những thứ khác biệt (do phần cứng)

1. ❌ **TPOT 80ms vs 10ms** — RTX 3060 compute thấp hơn H100 ~8×
2. ❌ **Không có continuous batching** — bài toán implementation thú vị
3. ❌ **Không có prefix caching** — mỗi request tính lại system prompt từ đầu
4. ⚠️ **Quantization 4-bit vs FP8** — 4-bit chậm hơn do dequant overhead

### Lộ trình cải thiện (theo learning roadmap)

| Tuần | Nội dung | Impact |
|------|----------|--------|
| 7-8 | Tự quản lý KV cache → hiểu memory layout, PagedAttention | Kiến trúc |
| 9+ | Tự viết forward pass → hiểu attention, GQA, RoPE | Kiến trúc |
| Sau đó | Continuous batching → tăng throughput 3-5× | Hiệu năng |
| Sau đó | Prefix caching → giảm TTFT 90% cho multi-turn | Hiệu năng |
