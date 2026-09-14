"""
batch_engine.py — Continuous Batching Engine.

Xử lý batch requests: gom nhiều request → tokenize với padding →
sampling loop tự viết (batched forward) → stream token từng request NGAY KHI SINH RA
(thay vì chờ generate xong hết rồi mới trả).

Đây là bước trung gian trước khi tự quản lý KV cache (Tuần 7-8).
Sampling loop tự viết (`worker/sampling.py`) — mỗi request có sampling params riêng.
"""

import asyncio
import queue as stdlib_queue
import time
from threading import Thread
from typing import AsyncIterator, Optional

import torch

from .model.tokenizer import StreamingDecoder
from .sampling import SamplingParams, generate_tokens

DEVICE = "cuda" if torch.cuda.is_available() else "cpu"


class BatchEngine:
    """Xử lý batch inference requests.

    Gom nhiều request → chạy sampling loop tự viết với batched inputs
    (padding + attention mask) → stream token per request.
    """

    def __init__(self, model, tokenizer, hf_tokenizer=None):
        self.model = model
        self.tokenizer = tokenizer  # BPETokenizer tự viết
        self.hf_tokenizer = hf_tokenizer  # chỉ cho apply_chat_template (Jinja)

    def _build_prompt(self, messages: list[dict], tools: Optional[list[dict]] = None) -> str:
        """Build chat prompt từ messages cho một request."""
        formatted = []
        for msg in messages:
            role = msg.get("role", "user")
            content = msg.get("content", "")

            if role == "tool":
                formatted.append({
                    "role": "tool",
                    "tool_call_id": msg.get("tool_call_id", ""),
                    "content": content,
                })
                continue

            fm = {"role": role, "content": content}

            if msg.get("tool_calls"):
                fm["tool_calls"] = [
                    {
                        "id": tc["id"],
                        "type": "function",
                        "function": {
                            "name": tc["name"],
                            "arguments": tc.get("arguments", "{}"),
                        },
                    }
                    for tc in msg["tool_calls"]
                ]

            formatted.append(fm)

        # Chat template dùng HF (Jinja) — không dùng tokenizer tự viết
        try:
            prompt = self.hf_tokenizer.apply_chat_template(
                formatted,
                tools=tools,
                tokenize=False,
                add_generation_prompt=True,
            )
        except Exception:
            prompt = self.hf_tokenizer.apply_chat_template(
                formatted,
                tokenize=False,
                add_generation_prompt=True,
            )
        return prompt

    async def generate_batch(self, requests: list[dict]) -> AsyncIterator[tuple[str, dict]]:
        """Process batch of requests, yield (request_id, event_dict) pairs.

        Mỗi request là dict với keys:
          - request_id: str
          - messages: list[dict]
          - system_prompt: str (optional)
          - sampling_params: dict (max_tokens, temperature, top_p, top_k)
          - tools: list[dict] (optional)

        Yields events:
          ("req_1", {"type": "token", "token": "Hello"})
          ("req_2", {"type": "token", "token": "Hi"})
          ...
          ("req_1", {"type": "final", "stop_reason": "STOP_END_TURN", ...})
        """
        if not requests:
            return

        n = len(requests)
        print(f"[batch_engine] Processing batch of {n} requests")

        # ── 1. Build prompts + sampling params cho từng request ──
        prompts = []
        request_ids = []
        params_list = []

        for req in requests:
            msgs = list(req.get("messages", []))
            system_prompt = req.get("system_prompt", "")
            tools = req.get("tools")

            # Prepend system prompt nếu có
            if system_prompt:
                msgs = [{"role": "system", "content": system_prompt}] + msgs

            prompt = self._build_prompt(msgs, tools)
            prompts.append(prompt)
            request_ids.append(req.get("request_id", ""))
            # Mỗi request có sampling params riêng (không còn average temperature)
            params_list.append(SamplingParams.from_dict(req.get("sampling_params")))

        max_tokens_list = [p.max_tokens for p in params_list]

        # ── 2. Tokenize toàn bộ batch với padding (tokenizer tự viết) ──
        t0 = time.perf_counter()
        inputs = self.tokenizer.build_inputs(prompts, max_length=8192)
        inputs = {k: v.to(DEVICE) for k, v in inputs.items()}

        prompt_lens = (inputs["attention_mask"] == 1).sum(dim=1).tolist()
        stop_ids = {self.tokenizer.eos_token_id, self.tokenizer.pad_token_id}

        # ── 3. Chạy sampling loop tự viết → token về NGAY khi sinh ──
        out_queue = stdlib_queue.Queue()
        finished = [False] * n
        completion_tokens = [0] * n
        gen_error = []

        def _run_generation():
            try:
                decoders = [
                    StreamingDecoder(self.tokenizer, skip_special_tokens=True)
                    for _ in range(n)
                ]
                with torch.no_grad():
                    for row, tid, reason in generate_tokens(
                        self.model,
                        inputs["input_ids"],
                        inputs["attention_mask"],
                        params_list,
                        stop_ids,
                    ):
                        if reason == "stop":
                            out_queue.put(("done", row))
                            continue
                        text = decoders[row].put(tid)
                        if text:
                            out_queue.put(("token", row, text))
                        if reason == "length":
                            out_queue.put(("done", row))
                # Đảm bảo mọi row đều có tín hiệu kết thúc (VD max_tokens=0)
                for i in range(n):
                    out_queue.put(("done", i))
            except Exception as e:  # noqa: BLE001 — chuyển lỗi sang async generator
                gen_error.append(e)

        gen_thread = Thread(target=_run_generation, daemon=True)
        gen_thread.start()

        def _final(i: int) -> dict:
            if completion_tokens[i] >= max_tokens_list[i]:
                stop_reason, finish_reason = "STOP_MAX_TOKENS", "length"
            else:
                stop_reason, finish_reason = "STOP_END_TURN", "stop"
            return {
                "type": "final",
                "stop_reason": stop_reason,
                "finish_reason": finish_reason,
                "usage": {
                    "prompt_tokens": prompt_lens[i],
                    "completion_tokens": completion_tokens[i],
                    "total_tokens": prompt_lens[i] + completion_tokens[i],
                },
            }

        # ── 4. Tiêu thụ queue: yield token mỗi khi generate ra ──
        remaining = n
        try:
            while remaining > 0:
                try:
                    # out_queue.get là call đồng bộ/blocking — đẩy sang thread để
                    # không chặn asyncio event loop (RPC khác vẫn chạy được).
                    item = await asyncio.to_thread(out_queue.get, True, 0.2)
                except stdlib_queue.Empty:
                    if gen_error:
                        raise gen_error[0]
                    if not gen_thread.is_alive():
                        # thread chết bất thường → đóng các request còn lại
                        for i in range(n):
                            if not finished[i]:
                                finished[i] = True
                                remaining -= 1
                                yield (request_ids[i], _final(i))
                        break
                    continue

                kind, i = item[0], item[1]
                if kind == "token":
                    text = item[2]
                    if finished[i]:
                        continue
                    completion_tokens[i] += 1
                    yield (request_ids[i], {"type": "token", "token": text})
                    if completion_tokens[i] >= max_tokens_list[i]:
                        # đạt max token của request này — cắt sớm, batch vẫn chạy cho request khác
                        finished[i] = True
                        remaining -= 1
                        yield (request_ids[i], _final(i))
                else:  # "done"
                    if not finished[i]:
                        finished[i] = True
                        remaining -= 1
                        yield (request_ids[i], _final(i))
        finally:
            # Normal: thread đã xong → join trả ngay. Cancel: chờ tối đa 0.5s rồi bỏ
            # (daemon thread tự kết thúc khi generate xong).
            gen_thread.join(timeout=0.5)

        elapsed = (time.perf_counter() - t0) * 1000
        total_new_tokens = sum(completion_tokens)
        print(f"[batch_engine] Batch done: {n} requests, {total_new_tokens} tokens, "
              f"{elapsed:.0f}ms, throughput={total_new_tokens/(elapsed/1000):.1f} tok/s")
