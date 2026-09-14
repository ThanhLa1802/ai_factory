"""Continuous batching engine — KV cache tự quản + iteration-level scheduling.

Khác `BatchEngine` (static: tập sequence cố định, HF quản `past_key_values`),
engine này là một daemon thread sở hữu model, mỗi iteration:

    admit (budget) → prefill batch → decode 1 bước toàn bộ active → evict

Sequence mới vào được **giữa lúc** sequence khác đang decode. Cache per-sequence
do `KVCache`/`KVCacheManager` quản (xem `worker/kv_cache.py`); HF chỉ chạy
attention cho một bước.

Contract giữ nguyên: `generate_batch(requests) -> AsyncIterator[(request_id, event)]`.
"""

import asyncio
import queue as stdlib_queue
import threading
from collections import deque
from dataclasses import dataclass
from typing import AsyncIterator, Optional

import torch

from .kv_cache import KVCache, KVCacheManager
from .model.tokenizer import StreamingDecoder
from .prompt import build_chat_prompt
from .sampling import SamplingParams, sample_next_batch

_IDLE_POLL = 0.01


@dataclass
class Sequence:
    request_id: str
    prompt_ids: torch.Tensor
    params: SamplingParams
    stop_ids: set
    stop_sequences: list
    decoder: object
    rpc_queue: stdlib_queue.Queue
    cancel_event: object = None
    cache: Optional[KVCache] = None
    pending_input: Optional[torch.Tensor] = None
    generated: int = 0
    text: str = ""
    finished: bool = False


class ContinuousBatchEngine:
    def __init__(
        self,
        model,
        tokenizer,
        hf_tokenizer=None,
        max_batch_size: int = 4,
        max_batch_tokens: int = 8192,
        device: Optional[str] = None,
        decoder_factory=None,
        prompt_builder=None,
    ):
        self.model = model
        self.tokenizer = tokenizer
        self.hf_tokenizer = hf_tokenizer
        self.max_batch_size = max_batch_size
        self.max_batch_tokens = max_batch_tokens
        self.device = device or getattr(model, "device", "cpu")
        self._decoder_factory = decoder_factory or (
            lambda: StreamingDecoder(self.tokenizer, skip_special_tokens=True)
        )
        self._prompt_builder = prompt_builder or build_chat_prompt
        self.kv = KVCacheManager()

        self._waiting: deque = deque()
        self._active: list[Sequence] = []
        self._cond = threading.Condition()
        self._thread: Optional[threading.Thread] = None
        self._running = False
        self._error: Optional[BaseException] = None

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def generate_batch(self, requests: list[dict]) -> AsyncIterator[tuple[str, dict]]:
        if not requests:
            return
        q: stdlib_queue.Queue = stdlib_queue.Queue()
        seqs = [self._make_sequence(r, q) for r in requests]
        self._enqueue(seqs)

        remaining = len(seqs)
        while remaining > 0:
            try:
                item = await asyncio.to_thread(q.get, True, 0.2)
            except stdlib_queue.Empty:
                if self._error is not None:
                    break
                if not self._is_running():
                    break
                continue
            request_id, event = item
            yield request_id, event
            if event["type"] == "final":
                remaining -= 1

        if self._error is not None:
            raise self._error

    def stop(self) -> None:
        """Dừng scheduler + finalize mọi sequence còn lại (shutdown)."""
        with self._cond:
            self._running = False
            self._cond.notify_all()
        if self._thread is not None:
            self._thread.join(timeout=2.0)
        for seq in list(self._waiting) + list(self._active):
            self._finish(seq, "STOP_CANCELLED", "cancelled")
        self._waiting.clear()
        self._active.clear()

    # ------------------------------------------------------------------
    # Submission / lifecycle
    # ------------------------------------------------------------------

    def _make_sequence(self, req: dict, q: stdlib_queue.Queue) -> Sequence:
        msgs = list(req.get("messages", []))
        system_prompt = req.get("system_prompt", "")
        if system_prompt:
            msgs = [{"role": "system", "content": system_prompt}] + msgs
        prompt = self._prompt_builder(self.hf_tokenizer, msgs, req.get("tools"))
        ids = torch.tensor(self.tokenizer.encode(prompt), dtype=torch.long, device=self.device)
        sp = req.get("sampling_params") or {}
        return Sequence(
            request_id=req.get("request_id", ""),
            prompt_ids=ids,
            params=SamplingParams.from_dict(sp),
            stop_ids={self.tokenizer.eos_token_id, self.tokenizer.pad_token_id},
            stop_sequences=list(sp.get("stop_sequences", [])),
            decoder=self._decoder_factory(),
            rpc_queue=q,
            cancel_event=req.get("cancel_event"),
        )

    def _enqueue(self, seqs: list[Sequence]) -> None:
        with self._cond:
            self._waiting.extend(seqs)
            self._cond.notify_all()
        self._ensure_started()

    def _ensure_started(self) -> None:
        with self._cond:
            if self._thread is None or not self._thread.is_alive():
                self._running = True
                self._error = None
                self._thread = threading.Thread(target=self._run_loop, daemon=True)
                self._thread.start()

    def _is_running(self) -> bool:
        return self._thread is not None and self._thread.is_alive()

    # ------------------------------------------------------------------
    # Scheduler loop (chạy trong daemon thread)
    # ------------------------------------------------------------------

    def _run_loop(self) -> None:
        try:
            while True:
                with self._cond:
                    if not self._running:
                        return
                    self._sweep_waiting_cancelled_locked()
                    if not self._waiting and not self._active:
                        self._cond.wait(timeout=_IDLE_POLL)
                        continue
                    new = self._take_waiting_locked()
                new = [s for s in new if not self._cancelled(s)]
                self._sweep_active_cancelled()
                if new:
                    self._prefill(new)
                    self._active.extend(new)
                if self._active:
                    self._decode_step()
        except Exception as e:  # noqa: BLE001 — chuyển lỗi sang mọi RPC đang chờ
            self._error = e
            with self._cond:
                self._cond.notify_all()

    def _take_waiting_locked(self) -> list[Sequence]:
        slots = self.max_batch_size - len(self._active)
        if slots <= 0:
            return []
        taken: list[Sequence] = []
        used = sum(int(s.prompt_ids.shape[0]) + max(s.params.max_tokens, 0) for s in self._active)
        while self._waiting and len(taken) < slots:
            seq = self._waiting[0]
            cost = int(seq.prompt_ids.shape[0]) + max(seq.params.max_tokens, 0)
            # Một request đơn lẻ luôn được nhận (chạy một mình) dù vượt budget.
            if (taken or self._active) and used + cost > self.max_batch_tokens:
                break
            self._waiting.popleft()
            taken.append(seq)
            used += cost
        return taken

    def _prefill(self, seqs: list[Sequence]) -> None:
        runnable = []
        for seq in seqs:
            if seq.params.max_tokens <= 0:
                self._finish(seq, "STOP_MAX_TOKENS", "length")
            else:
                runnable.append(seq)
        if not runnable:
            return

        si = self.kv.build_prefill([s.prompt_ids for s in runnable])
        with torch.no_grad():
            out = self.model(
                input_ids=si.input_ids,
                attention_mask=si.attention_mask,
                position_ids=si.position_ids,
                past_key_values=None,
                use_cache=True,
            )
        ids = sample_next_batch(out.logits[:, -1, :], [s.params for s in runnable])
        for i, seq in enumerate(runnable):
            cache = KVCache()
            cache.init_from_prefill(
                out.past_key_values, prompt_len=int(seq.prompt_ids.shape[0]), row=i
            )
            seq.cache = cache
            self._emit_or_finish(seq, int(ids[i].item()))

    def _decode_step(self) -> None:
        active = [s for s in self._active if not s.finished and s.pending_input is not None]
        if active:
            si = self.kv.build_decode(
                [s.cache for s in active], [s.pending_input for s in active]
            )
            with torch.no_grad():
                out = self.model(
                    input_ids=si.input_ids,
                    attention_mask=si.attention_mask,
                    position_ids=si.position_ids,
                    past_key_values=si.past_key_values,
                    use_cache=True,
                )
            ids = sample_next_batch(out.logits[:, -1, :], [s.params for s in active])
            for i, seq in enumerate(active):
                seq.cache.append_from_output(out.past_key_values, i)
                self._emit_or_finish(seq, int(ids[i].item()))
        self._active = [s for s in self._active if not s.finished]

    # ------------------------------------------------------------------
    # Token → event / termination
    # ------------------------------------------------------------------

    def _emit_or_finish(self, seq: Sequence, tid: int) -> None:
        if tid in seq.stop_ids:
            self._finish(seq, "STOP_END_TURN", "stop")
            return

        seq.generated += 1
        text = seq.decoder.put(tid)
        candidate = seq.text + text
        hit = next((s for s in seq.stop_sequences if s in candidate), None)
        if hit:
            cut = candidate.find(hit)
            if cut > len(seq.text):
                self._send(seq, {"type": "token", "token": candidate[len(seq.text) : cut]})
            seq.text = candidate
            self._finish(seq, "STOP_END_TURN", "stop")
            return

        seq.text = candidate
        if text:
            self._send(seq, {"type": "token", "token": text})
        if seq.generated >= seq.params.max_tokens:
            self._finish(seq, "STOP_MAX_TOKENS", "length")
            return
        seq.pending_input = torch.tensor([tid], dtype=torch.long, device=self.device)

    def _finish(self, seq: Sequence, stop_reason: str, finish_reason: str) -> None:
        if seq.finished:
            return
        seq.finished = True
        if seq.cache is not None:
            seq.cache.free()
            seq.cache = None
        prompt_tokens = int(seq.prompt_ids.shape[0])
        self._send(seq, {
            "type": "final",
            "stop_reason": stop_reason,
            "finish_reason": finish_reason,
            "usage": {
                "prompt_tokens": prompt_tokens,
                "completion_tokens": seq.generated,
                "total_tokens": prompt_tokens + seq.generated,
            },
        })

    def _send(self, seq: Sequence, event: dict) -> None:
        seq.rpc_queue.put((seq.request_id, event))

    def _cancelled(self, seq: Sequence) -> bool:
        return seq.cancel_event is not None and seq.cancel_event.is_set()

    def _sweep_waiting_cancelled_locked(self) -> None:
        if not self._waiting:
            return
        keep: deque = deque()
        for seq in self._waiting:
            if self._cancelled(seq):
                self._finish(seq, "STOP_CANCELLED", "cancelled")
            else:
                keep.append(seq)
        self._waiting = keep

    def _sweep_active_cancelled(self) -> None:
        for seq in list(self._active):
            if not seq.finished and self._cancelled(seq):
                self._finish(seq, "STOP_CANCELLED", "cancelled")
        self._active = [s for s in self._active if not s.finished]
