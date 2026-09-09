"""Benchmark llama engine (Qwen3.5-9B GGUF) qua llama-server HTTP.

Khác benchmark.py (transformers): llama không load model trong process, chạy qua
llama-server (process riêng) bằng OpenAI-compatible /v1/chat/completions. Đo từ phía
client: TTFT, TPOT (avg/p50), throughput, context scaling, concurrency scaling.

Chạy: python benchmark_llama.py   (llama-server phải đang chạy ở 127.0.0.1:8081)
"""
import asyncio
import json
import statistics
import sys
import time

import httpx

# Đảm bảo stdout/stderr dùng UTF-8 (tránh UnicodeEncodeError trên console cp1252 của Windows)
try:
    sys.stdout.reconfigure(encoding="utf-8")
    sys.stderr.reconfigure(encoding="utf-8")
except Exception:
    pass

BASE = "http://127.0.0.1:8081"
MODEL = "qwen3.5-9b"


def body(prompt, max_tokens=512, temperature=0.7, top_p=0.9, top_k=50):
    return {
        "model": MODEL,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "temperature": temperature,
        "top_p": top_p,
        "top_k": top_k,
        "stream": True,
        "stream_options": {"include_usage": True},
    }


async def stream_once(b):
    """Stream một request, trả dict các chỉ số đo được."""
    t0 = time.perf_counter()
    first = None          # token đầu tiên (bất kỳ: reasoning hoặc content)
    first_content = None  # token content đầu tiên (sau khi hết reasoning)
    inter = []            # khoảng cách giữa các token liên tiếp (s)
    last = t0
    n_text = 0
    usage = {}
    finish = None
    n_content = 0
    n_reasoning = 0

    async with httpx.AsyncClient(base_url=BASE, timeout=httpx.Timeout(600, connect=10)) as c:
        async with c.stream("POST", "/v1/chat/completions", json=b) as r:
            if r.status_code != 200:
                text = await r.aread()
                raise RuntimeError(f"HTTP {r.status_code}: {text.decode(errors='replace')[:400]}")
            async for line in r.aiter_lines():
                line = line.strip()
                if not line.startswith("data:"):
                    continue
                p = line[len("data:"):].strip()
                if not p or p == "[DONE]":
                    continue
                obj = json.loads(p)
                if obj.get("usage"):
                    usage = obj["usage"]
                choices = obj.get("choices") or []
                if not choices:
                    continue
                if choices[0].get("finish_reason"):
                    finish = choices[0]["finish_reason"]
                d = choices[0].get("delta") or {}
                reasoning = d.get("reasoning_content") or ""
                content = d.get("content") or ""
                text = reasoning + content
                if text:
                    now = time.perf_counter()
                    if first is None:
                        first = now
                    if content and first_content is None:
                        first_content = now
                    if n_text > 0:
                        inter.append(now - last)
                    n_text += 1
                    if reasoning:
                        n_reasoning += 1
                    if content:
                        n_content += 1
                    last = now

    total = time.perf_counter() - t0
    ttft_ms = (first - t0) * 1000 if first is not None else None
    ttfc_ms = (first_content - t0) * 1000 if first_content is not None else None
    comp_tokens = usage.get("completion_tokens", n_text) or n_text
    prompt_tokens = usage.get("prompt_tokens", 0)

    return {
        "finish": finish,
        "prompt_tokens": prompt_tokens,
        "completion_tokens": comp_tokens,
        "n_text_events": n_text,
        "n_reasoning": n_reasoning,
        "n_content": n_content,
        "ttft_ms": ttft_ms,
        "ttfc_ms": ttfc_ms,
        "tpot_avg_ms": (statistics.mean(inter) * 1000) if inter else None,
        "tpot_p50_ms": (statistics.median(inter) * 1000) if inter else None,
        "total_s": total,
        "throughput_tps": comp_tokens / total if total else 0,
    }


def fmt_latency(name, r):
    print(f"\n--- {name} ---")
    print(f"  prompt={r['prompt_tokens']} tok | completion={r['completion_tokens']} tok "
          f"(reasoning {r['n_reasoning']} / content {r['n_content']}) | finish={r['finish']}")
    print(f"  TTFT  (token đầu):      {r['ttft_ms']:.0f} ms")
    if r["ttfc_ms"] is not None:
        print(f"  TTFC  (content đầu):    {r['ttfc_ms']:.0f} ms  ← người dùng thực sự chờ")
    print(f"  TPOT  avg / p50:        {r['tpot_avg_ms']:.1f} / {r['tpot_p50_ms']:.1f} ms/token")
    print(f"  Throughput:             {r['throughput_tps']:.1f} tok/s")
    print(f"  Total:                  {r['total_s']:.2f} s")


async def latency_bench():
    print("=" * 60)
    print("1. LATENCY — TTFT / TPOT / THROUGHPUT  (max_tokens=512, temp=0.7)")
    print("=" * 60)
    cases = [
        ("Short", "Hello, how are you?", 512),
        ("Medium", "Explain the difference between CPU and GPU in 3 paragraphs.", 512),
        ("Long", "Write a detailed technical documentation about transformer attention mechanisms including the math formulas.", 512),
    ]
    results = []
    for name, prompt, mt in cases:
        r = await stream_once(body(prompt, max_tokens=mt))
        fmt_latency(name, r)
        results.append((name, r))
    return results


async def context_bench():
    print("\n" + "=" * 60)
    print("2. CONTEXT LENGTH vs PREFILL  (max_tokens=1, temp=0 — TTFT ≈ prefill)")
    print("=" * 60)
    base = "Artificial intelligence and machine learning are transforming the world. "  # ~13 tok
    rows = []
    for target in [128, 256, 512, 1024, 2048, 4096]:
        prompt = base * (target // 13 + 1)
        r = await stream_once(body(prompt, max_tokens=1, temperature=0))
        print(f"  prompt={r['prompt_tokens']:>6} tok -> TTFT {r['ttft_ms']:>8.0f} ms")
        rows.append((r["prompt_tokens"], r["ttft_ms"]))
    return rows


async def concurrency_bench():
    print("\n" + "=" * 60)
    print("3. CONCURRENCY / BATCH SCALING  (max_tokens=256, temp=0.7)")
    print("=" * 60)
    prompt = "Explain quantum computing briefly."
    rows = []
    for n in [1, 2, 4]:
        t0 = time.perf_counter()
        rs = await asyncio.gather(*[stream_once(body(prompt, max_tokens=256)) for _ in range(n)])
        wall = time.perf_counter() - t0
        total_comp = sum(r["completion_tokens"] for r in rs)
        agg_tps = total_comp / wall
        ttft_avg = statistics.mean(r["ttft_ms"] for r in rs)
        per_req = statistics.mean(r["throughput_tps"] for r in rs)
        print(f"  concurrency {n}: wall {wall:.1f}s, {total_comp} tok, "
              f"agg {agg_tps:.1f} tok/s (per-req {per_req:.1f}), TTFT avg {ttft_avg:.0f} ms")
        rows.append({"n": n, "wall_s": wall, "total_tokens": total_comp,
                     "agg_tps": agg_tps, "per_req_tps": per_req, "ttft_avg_ms": ttft_avg})
    return rows


async def main():
    print("AI FACTORY — BENCHMARK: llama engine (Qwen3.5-9B GGUF)")
    print(f"target: {BASE}  model: {MODEL}")
    latency = await latency_bench()
    ctx = await context_bench()
    conc = await concurrency_bench()

    out = {"latency": [{"name": n, **r} for n, r in latency],
           "context": [{"prompt_tokens": p, "ttft_ms": t} for p, t in ctx],
           "concurrency": conc}
    with open("benchmark_llama.json", "w", encoding="utf-8") as f:
        json.dump(out, f, indent=2)
    print("\n[ok] saved benchmark_llama.json")


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        sys.exit(130)
