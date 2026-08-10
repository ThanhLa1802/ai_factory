"""
continuous_batching_demo.py — Demo thuật toán Continuous Batching.

Ý tưởng: Thay vì gom request thành batch cố định rồi chạy hết,
ta chạy từng decode step. Giữa các step, thêm request mới, bỏ request đã xong.
"""

import time
import asyncio
from dataclasses import dataclass, field
from typing import List, Optional

# ---------------------------------------------------------------------------
# Mô phỏng 1 request
# ---------------------------------------------------------------------------

@dataclass
class Request:
    id: str
    prompt: str
    prompt_tokens: int
    max_tokens: int

    # Trạng thái runtime
    generated: List[str] = field(default_factory=list)
    kv_cache: object = None      # Trong thật: K,V tensors cho từng layer
    finished: bool = False

    @property
    def token_count(self) -> int:
        return len(self.generated)

    @property
    def should_stop(self) -> bool:
        return self.token_count >= self.max_tokens


# ---------------------------------------------------------------------------
# Mô phỏng inference engine với Continuous Batching
# ---------------------------------------------------------------------------

class ContinuousBatchingEngine:
    """
    Mô phỏng cách vLLM/TensorRT-LLM chạy continuous batching.

    Mỗi bước:
      1. Prefill request mới (nếu có) — chạy 1 lần toàn bộ prompt
      2. Decode 1 token cho MỌI request đang active — chạy chung 1 batch
      3. Bỏ request đã finished
      4. Lặp lại
    """

    def __init__(self, tp_ms: float = 10.0):
        self.tp_ms = tp_ms  # Time per decode step (mô phỏng)

    async def serve(self, incoming: asyncio.Queue[Request]):
        """
        Main serving loop — chạy mãi mãi, nhận request mới qua queue.
        """
        active: List[Request] = []      # Requests đang được xử lý
        pending_prefill: List[Request] = []  # Request mới, chưa prefill

        step = 0
        while True:
            # 1. Nhận request mới (non-blocking)
            while not incoming.empty():
                try:
                    req = incoming.get_nowait()
                    pending_prefill.append(req)
                    print(f"  [Step {step}] NEW: {req.id} (prompt={req.prompt_tokens}t, max={req.max_tokens}t)")
                except asyncio.QueueEmpty:
                    break

            # 2. Prefill request mới
            #    Trong thực tế: chạy model 1 lần với toàn bộ prompt tokens
            #    → tính K,V cho từng prompt token → lưu vào KV cache
            for req in pending_prefill:
                await asyncio.sleep(self.tp_ms * req.prompt_tokens / 1000)  # Mô phỏng prefill time
                active.append(req)
                print(f"  [Step {step}] PREFILL: {req.id} done ({req.prompt_tokens} tokens)")
            pending_prefill.clear()

            if not active:
                # Không có request nào — đợi
                await asyncio.sleep(0.01)
                step += 1
                continue

            # 3. Decode 1 token cho TẤT CẢ active requests
            #    Đây là điểm khác biệt: tất cả requests decode CÙNG 1 BƯỚC
            #    Mỗi request:
            #      - Dùng KV cache của nó
            #      - Sinh 1 token mới
            #      - Lưu K,V mới vào cache của nó
            await asyncio.sleep(self.tp_ms / 1000)  # 1 decode step (~10ms)

            to_remove = []
            for req in active:
                req.generated.append(f"tok_{step}")  # Mô phỏng sinh token

                if req.should_stop:
                    to_remove.append(req)

            # 4. Bỏ request đã xong khỏi batch
            for req in to_remove:
                active.remove(req)
                print(f"  [Step {step}] DONE: {req.id} ({req.token_count} tokens generated)")

            if active:
                batch_info = ", ".join(f"{r.id}({r.token_count}/{r.max_tokens})" for r in active)
                print(f"  [Step {step}] DECODE batch={len(active)}: [{batch_info}]")

            step += 1


# ---------------------------------------------------------------------------
# Demo: So sánh Static vs Continuous Batching
# ---------------------------------------------------------------------------

async def demo():
    print("=" * 60)
    print("CONTINUOUS BATCHING — DEMO")
    print("=" * 60)

    # Mô phỏng requests đến không cùng lúc
    # Request A: prompt ngắn, max 8 tokens
    # Request B: prompt ngắn, max 5 tokens (đến sau A 2 step)
    # Request C: prompt dài, max 6 tokens (đến sau B 1 step)
    incoming: asyncio.Queue[Request] = asyncio.Queue()

    engine = ContinuousBatchingEngine(tp_ms=50)  # 50ms/decode để dễ thấy

    # Schedule requests đến theo thời gian
    async def arrival_schedule():
        await incoming.put(Request("A", "Hello", prompt_tokens=5, max_tokens=8))
        await asyncio.sleep(0.15)  # Đến sau 3 step
        await incoming.put(Request("B", "Hi", prompt_tokens=3, max_tokens=5))
        await asyncio.sleep(0.05)  # Đến sau 1 step
        await incoming.put(Request("C", "Long question...", prompt_tokens=20, max_tokens=6))

    # Chạy engine + schedule song song
    await asyncio.gather(engine.serve(incoming), arrival_schedule())


# ---------------------------------------------------------------------------
# So sánh latency
# ---------------------------------------------------------------------------

def compare():
    print("\n" + "=" * 60)
    print("SO SÁNH LATENCY: Static vs Continuous Batching")
    print("=" * 60)

    # Giả sử 3 requests: A(8 tok), B(5 tok), C(6 tok)
    # Mỗi decode step = 10ms
    # B đến sau A 2 step, C đến sau B 1 step

    tp = 10  # ms per decode step

    # === STATIC BATCHING ===
    # A chạy 1 mình (8 steps), B+C chờ
    # Rồi B+C chạy cùng (max(5,6)=6 steps)
    static_latency = {
        "A": 8 * tp,
        "B": 8 * tp + 6 * tp,  # Đợi A xong + chạy 6 step
        "C": 8 * tp + 6 * tp,
    }
    static_total = sum(static_latency.values())

    # === CONTINUOUS BATCHING ===
    # Step 1-2: A chạy 1 mình (2 steps)
    # Step 3: B join → A,B chạy (A còn 6, B còn 5)
    # Step 4: C join → A,B,C chạy (A còn 5, B còn 4, C còn 6)
    # Step 5-8: A chạy 4 step → done (step 8)
    # Step 9: B done (step 9)
    # Step 10: C done (step 10)
    cb_latency = {
        "A": 8 * tp,
        "B": (8 - 2) * tp + 2 * tp,  # 6 step chạy + 2 step đợi = 8
        "C": (10 - 3) * tp + 3 * tp,  # 7 step chạy + 3 step đợi = 10
    }
    cb_total = sum(cb_latency.values())

    # Actually let me recalculate properly
    # Continuous:
    # Steps: 1=A, 2=A, 3=A+B, 4=A+B+C, 5=A+B+C, 6=A+B+C, 7=A+B+C, 8=A+B+C(A done), 9=B+C(B done), 10=C
    cb_latency_correct = {
        "A": 8 * tp,       # Done at step 8
        "B": (9 - 3) * tp + 3 * tp,  # Done at step 9 = 9 steps from arrival at step 3 = 6 steps
        "C": (10 - 4) * tp + 4 * tp,  # Done at step 10 = 10 steps from arrival at step 4 = 6 steps
    }

    print(f"""
{'Metric':<25} {'Static':>10} {'Continuous':>12} {'Improvement':>14}
{'-'*60}
""")
    for req_id in ["A", "B", "C"]:
        s = static_latency[req_id]
        c = cb_latency_correct[req_id]
        imp = (s - c) / s * 100
        print(f"  {req_id} latency              {s:>6} ms    {c:>6} ms       {imp:>8.0f}% faster")

    print(f"  {'─'*50}")
    print(f"  Total wait time         {static_total:>6} ms    {sum(cb_latency_correct.values()):>6} ms       {(static_total - sum(cb_latency_correct.values())) / static_total * 100:>8.0f}% faster")
    print(f"  GPU compute steps       {8+6:>6} steps  {10:>6} steps       {(8+6-10)/(8+6)*100:>8.0f}% less GPU time")
    print()
    print("  → Continuous batching: latency THẤP HƠN + GPU dùng ÍT BƯỚC HƠN")
    print("  → GPU compute steps giảm vì không phải chạy riêng từng batch")


if __name__ == "__main__":
    compare()
    asyncio.run(demo())
