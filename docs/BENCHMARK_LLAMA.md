# AI Factory — Benchmark Report: llama engine (Qwen3.5-9B GGUF)

**Date:** 2026-09-05
**Model:** Qwen3.5-9B (GGUF, Q4_K - Medium)
**GPU:** NVIDIA GeForce RTX 3060 12GB VRAM
**Runtime:** llama.cpp (`llama-server`, build b10342) — OpenAI-compatible `/v1/chat/completions`

> Benchmark đo từ **phía client** (HTTP streaming), khác `docs/BENCHMARK.md` (transformers đo trong-process). Script: `python-worker/benchmark_llama.py`; số liệu thô: `python-worker/benchmark_llama.json`.

---

## 0. Model & hardware

| Thuộc tính | Giá trị |
|---|---|
| Params | 8,953,803,264 (~8.95B) |
| `n_embd` / `n_vocab` | 4096 / 248 320 |
| `n_ctx` (context window) | 8192 (train 262 144) |
| Quantization | Q4_K - Medium (file ~5.28 GiB) |
| Slots song song | 4 (`--parallel 4`) |
| Reasoning format | `deepseek` (`<think>…</think>`) |

**VRAM (idle, `nvidia-smi`):** tổng đã dùng ~6847 MiB / 12288 MiB — trong đó model weights ~5.4 GiB + KV cache + CUDA context. (llama-server chạy dưới LocalSystem nên `nvidia-smi` không đọc được VRAM theo process, chỉ có số tổng.)

---

## 1. Latency — TTFT / TPOT / Throughput

Điều kiện: `max_tokens=512`, `temperature=0.7`, `top_p=0.9`, `top_k=50` (đúng default của worker).

| Scenario | Prompt (tok) | Completion (tok) | Reasoning / Content | TTFT | TTFC | TPOT avg | TPOT p50 | Throughput |
|----------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| Short | 16 | 345 | 316 / 26 | 686 ms | 7 738 ms | 22.3 ms | 22.1 ms | 41.5 tok/s |
| Medium | 23 | 512 | 512 / 0 | 160 ms | ∞ (0 content) | 20.0 ms | 19.6 ms | 49.2 tok/s |
| Long | 24 | 512 | 512 / 0 | 116 ms | ∞ (0 content) | 19.7 ms | 19.6 ms | 50.3 tok/s |

### Các chỉ số nghĩa là gì

| Chỉ số | Ý nghĩa | Công thức |
|--------|---------|-----------|
| **TTFT** | *Time To First Token* — từ lúc gửi request tới token đầu tiên. Gồm **prefill** (xử lý toàn bộ prompt trong 1 lượt) + 1 bước decode | `t_first_token − t_request` |
| **TTFC** | *Time To First Content* — tới token **nội dung** đầu tiên (sau khi model ngừng suy nghĩ). **Đây là độ trễ người dùng thực sự cảm nhận** với reasoning model | `t_first_content − t_request` |
| **TPOT** | *Time Per Output Token* — thời gian trung bình sinh 1 token (không tính token đầu). `avg` = trung bình, `p50` = median | `(t_last − t_first) / (n − 1)` |
| **Throughput** | Tổng token / tổng thời gian của cả request | `completion_tokens / total_time` |

### Hai phát hiện quan trọng

**1. Đây là *reasoning model* — gần như toàn bộ token là "suy nghĩ".**

- Short: 316 token reasoning vs **26 token content**.
- Medium / Long: **512/512 token là reasoning, 0 content** → model hết `max_tokens` ngay trong pha suy nghĩ, **chưa kịp trả lời** (`finish=length`).

Hệ quả thực dụng: với `max_tokens` nhỏ (vd 512), câu hỏi dài sẽ "im lặng" — không ra chữ nào. Cần `max_tokens` đủ lớn (≥ ~1024) để vừa nghĩ vừa trả lời. (Đây chính là follow-up đã ghi trong ledger: backend chỉ đọc `delta.content`, bỏ qua `delta.reasoning_content`.)

**2. TTFT có hiệu ứng cold-start.** Short chạy đầu tiên nên TTFT = 686 ms (khởi tạo slot + warmup CUDA kernel); các request sau ấm lên còn 116–160 ms. So sánh TTFT phải dùng giá trị *warm*.

---

## 2. Context Length vs Prefill

`max_tokens=1`, `temperature=0` → TTFT ≈ chi phí **prefill** (xử lý prompt), không có decode dài.

| Prompt (tok) | TTFT (prefill) |
|---:|---:|
| 111 | 186 ms |
| 211 | 203 ms |
| 411 | 342 ms |
| 801 | 576 ms |
| 1 591 | 904 ms |
| 3 171 | 1 411 ms |

> Prefill tăng **gần tuyến tính** nhưng dốc nông: 3 171 tok (gấp ~28× so với 111) chỉ mất 1 411 ms (gấp ~7.6×). GPU xử lý prefill song song nên chi phí/token giảm dần (~1.7 ms/tok ở 111 tok → ~0.44 ms/tok ở 3 171 tok).

---

## 3. Concurrency / Batch Scaling

`max_tokens=256`, 4 slot song song của llama-server.

| Concurrency | Wall time | Total tokens | Aggregate tok/s | Per-request tok/s | TTFT avg |
|---:|---:|---:|---:|---:|---:|
| 1 | 5.6 s | 256 | 45.6 | 45.6 | 313 ms |
| 2 | 7.1 s | 512 | 72.0 | 36.1 | 265 ms |
| 4 | 11.5 s | 1024 | 88.7 | 22.2 | 616 ms |

> Tăng concurrency 1→4 gần như **gấp đôi** aggregate throughput (45.6 → 88.7 tok/s, ~1.94×), không phải 4×. Điều đó cho thấy RTX 3060 bị under-utilized ở single stream, nhưng 4 slot song song đã tiến tới bão hoà (~89 tok/s). Per-request throughput giảm (45.6 → 22.2 tok/s) và TTFT tăng ở mức 4 (616 ms) do tranh chấp GPU — trade-off kinh điển giữa *throughput hệ thống* và *latency từng request*.

---

## 4. So sánh: llama vs transformers

| Metric | transformers (Qwen2.5-Coder-7B, NF4) | **llama (Qwen3.5-9B, GGUF Q4_K_M)** |
|---|---|---|
| TTFT (warm, short prompt) | 70–85 ms | ~116–160 ms |
| TPOT | 75–82 ms/token | **~20 ms/token** |
| Throughput (single) | 10.9–13.1 tok/s | **41–50 tok/s** |
| Throughput (4-way) | 53.8 tok/s | **88.7 tok/s** |

> **Điểm nổi bật:** llama.cpp (GGUF Q4_K_M, CUDA) decode **nhanh hơn ~3.5–4×** so với transformers + bitsandbytes NF4. Lý do chính: NF4 có overhead dequant mỗi lần forward, còn GGUF Q4_K_M đã có kernel decode tối ưu sẵn trên CUDA. Lưu ý đây là 2 model khác nhau (7B vs 9B) nên so sánh mang tính *runtime*, không phải *model*.

---

## 5. Tóm tắt: điều gì đáng chú ý

1. ✅ **Decode nhanh** — TPOT ~20 ms/token, throughput ~45–50 tok/s (single), ~89 tok/s (4-way).
2. ⚠️ **Reasoning chiếm gần hết budget token** — bài toán thật cho UI/loop: phải đợi hết pha nghĩ mới thấy câu trả lời (TTFC 7.7 s ở short prompt), và `max_tokens` nhỏ sẽ khiến model không trả lời được câu dài.
3. ⚠️ **Backend đang bỏ `reasoning_content`** — Go/UI chỉ nhận `delta.content` nên người dùng không thấy quá trình "nghĩ", và usage `completion_tokens` đếm cả phần reasoning (không chỉ câu trả lời thật).
4. ⚠️ **Cold-start TTFT** — request đầu tiên chậm hơn ~5× do warmup slot/CUDA kernel; cần đo warm để có số ổn định.
5. 🔜 **Chưa đo:** prefix caching (multi-turn reuses KV), tool-call latency (buffered 1 cục, không stream — xem ledger follow-up), `timings_per_token` server-side của llama-server (có thể bật để đối chiếu TPOT đo client vs server).
