"""
batch_engine.py — Continuous Batching Engine.

Xử lý batch requests: gom nhiều request → tokenize với padding →
model.generate() batch → stream token từng request NGAY KHI SINH RA
(thay vì chờ generate xong hết rồi mới trả).

Đây là bước trung gian trước khi tự quản lý KV cache (Tuần 7-8).
Hiện tại dùng HF model.generate() với batched inputs + custom streamer.
"""

import queue as stdlib_queue
import time
from threading import Thread
from typing import AsyncIterator, Optional

import torch
from transformers import TextStreamer

DEVICE = "cuda" if torch.cuda.is_available() else "cpu"


class BatchTokenStreamer(TextStreamer):
    """Streamer cho batched generate — phát token mới của TỪNG request theo thời gian thực.

    Contract của `model.generate(streamer=...)` (xác minh từ transformers 4.50.3):
      - gọi `put(input_ids)` MỘT LẦN với toàn bộ prompt → bỏ qua (không phải token sinh ra)
      - mỗi decode step: `put(next_tokens)` shape [batch, 1] → token mới của mỗi request
      - cuối: `end()`

    Request sinh ra eos (hoặc bị mask thành pad vì đã xong nhưng batch còn chạy)
    → đánh dấu done. Event đẩy vào `out_queue` (queue.Queue thread-safe), phía
    async generator tiêu thụ để stream về client mà không cần chờ hết batch.
    """

    def __init__(self, tokenizer, out_queue, n, eos_id, pad_id):
        super().__init__(tokenizer)
        self.out_queue = out_queue
        self.n = n
        self.eos_id = eos_id
        self.pad_id = pad_id
        self.active = [True] * n
        self._first = True  # lần put đầu là prompt, không phải token mới

    def put(self, value):
        if value.dim() == 1:
            # next_tokens từ _sample đã squeeze(1) → shape [batch]; cần [batch, 1]
            value = value.unsqueeze(-1)
        if self._first:
            self._first = False
            return
        for i in range(self.n):
            if not self.active[i]:
                continue
            tid = int(value[i, -1].item())
            if tid == self.eos_id or tid == self.pad_id:
                self.active[i] = False
                self.out_queue.put(("done", i))
                continue
            text = self.tokenizer.decode([tid], skip_special_tokens=True)
            if text:
                self.out_queue.put(("token", i, text))

    def end(self):
        # generate kết thúc (đạt max_new_tokens) — request còn active đánh dấu done
        for i in range(self.n):
            if self.active[i]:
                self.active[i] = False
                self.out_queue.put(("done", i))

    def on_finalized_text(self, text, stream_end=False):
        # tắt in ra console mặc định của TextStreamer
        pass


class BatchEngine:
    """Xử lý batch inference requests.

    Gom nhiều request → chạy model.generate() với batched inputs
    (padding + attention mask) → split output → stream token per request.
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

        # ── 1. Build prompts cho từng request ──
        prompts = []
        request_ids = []
        max_tokens_list = []
        temperatures = []

        for req in requests:
            msgs = list(req.get("messages", []))
            system_prompt = req.get("system_prompt", "")
            tools = req.get("tools")
            sp = req.get("sampling_params", {})

            # Prepend system prompt nếu có
            if system_prompt:
                msgs = [{"role": "system", "content": system_prompt}] + msgs

            prompt = self._build_prompt(msgs, tools)
            prompts.append(prompt)
            request_ids.append(req.get("request_id", ""))
            max_tokens_list.append(sp.get("max_tokens", 1024))
            temperatures.append(sp.get("temperature", 0.7))

        # ── 2. Tokenize toàn bộ batch với padding (tokenizer tự viết) ──
        t0 = time.perf_counter()
        inputs = self.tokenizer.build_inputs(prompts, max_length=8192)
        inputs = {k: v.to(DEVICE) for k, v in inputs.items()}

        prompt_lens = (inputs["attention_mask"] == 1).sum(dim=1).tolist()
        max_new_tokens = max(max_tokens_list)
        avg_temperature = sum(temperatures) / len(temperatures) if temperatures else 0.7

        # ── 3. Chạy model.generate() với streamer → token về NGAY khi sinh ──
        out_queue = stdlib_queue.Queue()
        streamer = BatchTokenStreamer(
            self.tokenizer, out_queue, n,
            eos_id=self.tokenizer.eos_token_id,
            pad_id=self.tokenizer.pad_token_id,
        )

        finished = [False] * n
        completion_tokens = [0] * n
        gen_error = []

        def _run_generation():
            try:
                with torch.no_grad():
                    self.model.generate(
                        input_ids=inputs["input_ids"],
                        attention_mask=inputs["attention_mask"],
                        max_new_tokens=max_new_tokens,
                        do_sample=avg_temperature > 0,
                        temperature=avg_temperature if avg_temperature > 0 else 1.0,
                        top_p=0.9,
                        top_k=50,
                        pad_token_id=self.tokenizer.pad_token_id,
                        eos_token_id=self.tokenizer.eos_token_id,
                        streamer=streamer,
                    )
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
                    item = out_queue.get(timeout=0.2)
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
