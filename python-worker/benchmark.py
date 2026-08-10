"""
Benchmark AI Factory inference engine.
Measures: TTFT, TPOT, throughput, KV cache scaling, prefill vs decode.
"""
import time
import torch
from transformers import AutoTokenizer, AutoModelForCausalLM, BitsAndBytesConfig, AutoConfig

MODEL_ID = "Qwen/Qwen2.5-Coder-7B-Instruct"
MODEL_VRAM_4BIT = 4.2  # ~4-bit 7B — sẽ được thay bằng giá trị đo được ở mục 2

# Load config for theoretical analysis
config = AutoConfig.from_pretrained(MODEL_ID)

print("=" * 60)
print("AI FACTORY — BENCHMARK REPORT")
print("=" * 60)
print(f"Model:   {MODEL_ID}")
print(f"Device:  {torch.cuda.get_device_name(0)}")
print(f"VRAM:    {torch.cuda.get_device_properties(0).total_memory / 1e9:.1f} GB")

# =========================================================================
# 1. KV CACHE THEORETICAL ANALYSIS
# =========================================================================
print("\n" + "=" * 60)
print("1. KV CACHE MEMORY MODEL")
print("=" * 60)

L = config.num_hidden_layers          # 36
H_kv = config.num_key_value_heads     # 2 (Grouped Query Attention)
D = config.hidden_size // config.num_attention_heads  # 128
BYTES = 2  # bfloat16

kv_per_layer = 2 * H_kv * D * BYTES  # K + V bytes per layer per token
kv_per_token = L * kv_per_layer       # total bytes per token
kv_per_token_mb = kv_per_token / 1e6

print(f"Architecture:  {L} layers, GQA {config.num_attention_heads}:{H_kv}")
print(f"K+V per layer: {kv_per_layer} bytes = {kv_per_layer/1024:.1f} KB")
print(f"KV per token:  {kv_per_token} bytes = {kv_per_token_mb:.4f} MB")

print(f"\n{'Context':>10} {'KV Cache':>12} {'+Model(4.2GB)':>15} {'%VRAM':>8}")
print("-" * 50)
for ctx in [256, 512, 1024, 2048, 4096, 8192, 16384, 32768]:
    kv_gb = (kv_per_token * ctx) / 1e9
    total = MODEL_VRAM_4BIT + kv_gb   # model ~4.2GB in 4-bit (7B Coder)
    pct = total / 12.9 * 100
    print(f"{ctx:>10} {kv_gb:>9.3f} GB {total:>12.2f} GB {pct:>7.1f}%")

# =========================================================================
# 2. LOAD MODEL & MEASURE ACTUAL VRAM
# =========================================================================
print("\n" + "=" * 60)
print("2. ACTUAL VRAM MEASUREMENT (loading model...)")
print("=" * 60)

torch.cuda.empty_cache()
torch.cuda.reset_peak_memory_stats()

bnb_config = BitsAndBytesConfig(
    load_in_4bit=True,
    bnb_4bit_compute_dtype=torch.bfloat16,
    bnb_4bit_use_double_quant=True,
    bnb_4bit_quant_type="nf4",
)

tokenizer = AutoTokenizer.from_pretrained(MODEL_ID)
if tokenizer.pad_token is None:
    tokenizer.pad_token = tokenizer.eos_token

model = AutoModelForCausalLM.from_pretrained(
    MODEL_ID,
    quantization_config=bnb_config,
    device_map="auto",
    trust_remote_code=True,
    torch_dtype=torch.bfloat16,
)
model.eval()

model_vram = torch.cuda.max_memory_allocated() / 1e9
print(f"Model VRAM (4-bit): {model_vram:.2f} GB")
print(f"KV / 1K tokens:     {kv_per_token * 1000 / 1e9:.3f} GB")
print(f"Available for KV:   {12.9 - model_vram - 1.0:.2f} GB (1GB safety)")
print(f"Max context:         {int((12.9 - model_vram - 1.0) * 1e9 / kv_per_token):,} tokens")

# =========================================================================
# 3. LATENCY BENCHMARKS
# =========================================================================
print("\n" + "=" * 60)
print("3. LATENCY BENCHMARKS")
print("=" * 60)

benchmarks = [
    ("Short", "Hello, how are you?", 50),
    ("Medium", "Explain the difference between CPU and GPU in 3 paragraphs.", 150),
    ("Long", "Write a detailed technical documentation about transformer attention mechanisms including the math formulas.", 300),
]

for name, prompt, max_tokens in benchmarks:
    inputs = tokenizer(prompt, return_tensors="pt").to("cuda")
    prompt_len = inputs.input_ids.shape[1]

    torch.cuda.synchronize()
    t0 = time.perf_counter()

    with torch.no_grad():
        output = model.generate(
            **inputs,
            max_new_tokens=max_tokens,
            do_sample=True,
            temperature=0.7,
            pad_token_id=tokenizer.eos_token_id,
        )

    torch.cuda.synchronize()
    elapsed = time.perf_counter() - t0

    new_tokens = output.shape[1] - prompt_len
    tps = new_tokens / elapsed

    # Get individual token timing via streaming
    streamer_start = time.perf_counter()
    ttft = None
    token_times = []

    with torch.no_grad():
        from transformers import TextIteratorStreamer
        from threading import Thread

        streamer = TextIteratorStreamer(tokenizer, skip_prompt=True, skip_special_tokens=True)
        gen_kwargs = {
            "input_ids": inputs.input_ids,
            "attention_mask": inputs.attention_mask,
            "max_new_tokens": max_tokens,
            "do_sample": True,
            "temperature": 0.7,
            "pad_token_id": tokenizer.eos_token_id,
            "streamer": streamer,
        }

        thread = Thread(target=model.generate, kwargs=gen_kwargs)
        thread.start()

        last_time = time.perf_counter()
        for token in streamer:
            now = time.perf_counter()
            if ttft is None:
                ttft = (now - last_time) * 1000  # ms
            else:
                token_times.append((now - last_time) * 1000)  # ms
            last_time = now

        thread.join()

    avg_tpot = sum(token_times) / len(token_times) if token_times else 0
    p50_tpot = sorted(token_times)[len(token_times)//2] if token_times else 0

    print(f"\n--- {name} (prompt={prompt_len} tokens, gen={max_tokens} tokens) ---")
    print(f"  Total time:      {elapsed*1000:.0f} ms")
    print(f"  TTFT:            {ttft:.0f} ms (time to first token)")
    print(f"  TPOT (avg):      {avg_tpot:.1f} ms/token")
    print(f"  TPOT (p50):      {p50_tpot:.1f} ms/token")
    print(f"  Throughput:      {tps:.1f} tokens/sec")
    print(f"  Gen tokens:      {new_tokens}")

# =========================================================================
# 4. CONTEXT LENGTH SCALING
# =========================================================================
print("\n" + "=" * 60)
print("4. CONTEXT LENGTH vs LATENCY (prefill phase)")
print("=" * 60)

context_sizes = [128, 256, 512, 1024, 2048, 4096]
# We'll use a fixed text padded/truncated to each size
base_text = "Artificial intelligence and machine learning are transforming the world. " * 200

for ctx_size in context_sizes:
    # Create prompt of exact token length
    tokens = tokenizer.encode(base_text, truncation=True, max_length=ctx_size)
    prompt_text = tokenizer.decode(tokens)

    inputs = tokenizer(prompt_text, return_tensors="pt").to("cuda")
    actual_len = inputs.input_ids.shape[1]

    # Measure prefill time
    torch.cuda.synchronize()
    t0 = time.perf_counter()

    with torch.no_grad():
        output = model.generate(**inputs, max_new_tokens=10, do_sample=False, pad_token_id=tokenizer.eos_token_id)

    torch.cuda.synchronize()
    elapsed = time.perf_counter() - t0

    print(f"  {actual_len:>6} tokens prompt -> {elapsed*1000:>8.0f} ms total (prefill + 10 tokens decode)")

# =========================================================================
# 5. REQUEST QUEUING / BATCHING PREVIEW
# =========================================================================
print("\n" + "=" * 60)
print("5. BATCH SIZE vs THROUGHPUT")
print("=" * 60)

prompts = [
    "What is 2+2?",
    "Explain quantum computing briefly.",
    "Write a haiku about coding.",
    "Describe the color blue.",
]

for batch_size in [1, 2, 4]:
    # Simulate batching with sequential + padding
    batch_prompts = prompts[:batch_size]

    tokenizer.pad_token = tokenizer.eos_token
    inputs = tokenizer(batch_prompts, return_tensors="pt", padding=True).to("cuda")

    torch.cuda.synchronize()
    t0 = time.perf_counter()

    with torch.no_grad():
        output = model.generate(**inputs, max_new_tokens=30, do_sample=False, pad_token_id=tokenizer.eos_token_id)

    torch.cuda.synchronize()
    elapsed = time.perf_counter() - t0

    total_new_tokens = sum(o.shape[0] - inputs.input_ids.shape[1] for o in output)
    tps = total_new_tokens / elapsed

    print(f"  Batch size {batch_size}: {elapsed*1000:.0f} ms, {tps:.1f} tokens/sec ({total_new_tokens} tokens)")

# =========================================================================
# SUMMARY
# =========================================================================
print("\n" + "=" * 60)
print("SUMMARY: KEY METRICS FOR PRODUCTION")
print("=" * 60)
print(f"""
| Metric               | Value              | Production Target |
|----------------------|-------------------|-------------------|
| Model                | {MODEL_ID.split('/')[1]:<20} | GLM 5.2 (100B+)   |
| VRAM (model)         | {model_vram:.1f} GB           | 200+ GB (multi-GPU)|
| KV / 1K tokens       | {kv_per_token*1000/1e9:.3f} GB         | ~3 GB (GQA H100)  |
| TTFT (short prompt)  | ~{ttft:.0f} ms           | <100 ms           |
| TPOT                 | ~{avg_tpot:.0f} ms/token     | <10 ms/token      |
| Max context          | {int((12.9-model_vram-1.0)*1e9/kv_per_token):,} tokens       | 128K-1M           |
| Throughput           | ~{tps:.0f} tok/sec       | 1000+ tok/sec     |
| Quantization         | 4-bit (NF4)        | FP8 (H100 native) |
| Batching             | Not supported yet  | Continuous Batching|
""")

# Cleanup
del model
torch.cuda.empty_cache()
print("Benchmark complete.")
