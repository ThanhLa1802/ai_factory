"""
Inference engine — wraps HuggingFace model for token generation.

Uses Qwen 2.5 3B Instruct with 4-bit quantization (bitsandbytes).
Tokenizer là BPETokenizer tự viết (byte-level BPE); HF `AutoTokenizer` chỉ được
giữ để build chat template (apply_chat_template) — quyết định D1 trong spec.
Handles: tokenization, forward pass, sampling, KV cache (via HF).
"""

import asyncio
import queue as stdlib_queue
from typing import AsyncIterator, Optional

import torch
from transformers import (
    AutoModelForCausalLM,
    AutoTokenizer,
    BitsAndBytesConfig,
)
from threading import Thread

from .model.tokenizer import BPETokenizer, StreamingDecoder
from .sampling import SamplingParams, generate_tokens

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

MODEL_ID = "Qwen/Qwen2.5-Coder-7B-Instruct"
# Ungated model — no HuggingFace login needed.
# Cùng tokenizer byte-level BPE với Qwen2.5-3B (vocab/merges sha trùng) →
# BPETokenizer tự viết chạy nguyên vẹn, chỉ đổi model weights.
# Alternatives:
#   "Qwen/Qwen2.5-3B-Instruct"
#   "meta-llama/Llama-3.2-3B-Instruct" (needs HF login)
#   "microsoft/Phi-3.5-mini-instruct"
#   "google/gemma-2-2b-it"

DEVICE = "cuda" if torch.cuda.is_available() else "cpu"

# 4-bit quantization config — vừa 12GB VRAM cho 3B model
BNB_CONFIG = BitsAndBytesConfig(
    load_in_4bit=True,
    bnb_4bit_compute_dtype=torch.bfloat16,
    bnb_4bit_use_double_quant=True,
    bnb_4bit_quant_type="nf4",
)

# ---------------------------------------------------------------------------
# Engine
# ---------------------------------------------------------------------------

class InferenceEngine:
    """Manages model lifecycle and provides generate() with token streaming."""

    def __init__(self, model_id: str = MODEL_ID):
        self.model_id = model_id
        self.model = None
        self.tokenizer = None
        self.hf_tokenizer = None  # chỉ dùng cho apply_chat_template (D1)
        self._loaded = False

    def load(self) -> None:
        """Load model and tokenizer into memory."""
        if self._loaded:
            return

        print(f"[engine] Loading tokenizer: {self.model_id}")
        # Own byte-level BPE tokenizer — thay thế HF tokenizer trong pipeline.
        self.tokenizer = BPETokenizer.from_pretrained(self.model_id)
        # Giữ HF chỉ để build chat template (Jinja) — không dùng để token hoá.
        self.hf_tokenizer = AutoTokenizer.from_pretrained(self.model_id)

        print(f"[engine] Loading model: {self.model_id} (4-bit, device={DEVICE})")
        self.model = AutoModelForCausalLM.from_pretrained(
            self.model_id,
            quantization_config=BNB_CONFIG,
            device_map="auto",
            trust_remote_code=True,
            torch_dtype=torch.bfloat16,
        )
        self.model.eval()
        self._loaded = True
        print(f"[engine] Model loaded. VRAM used: {torch.cuda.max_memory_allocated() / 1e9:.1f} GB")

    def unload(self) -> None:
        """Free model from memory."""
        if self.model is not None:
            del self.model
            self.model = None
        if self.tokenizer is not None:
            del self.tokenizer
            self.tokenizer = None
        if self.hf_tokenizer is not None:
            del self.hf_tokenizer
            self.hf_tokenizer = None
        self._loaded = False
        if torch.cuda.is_available():
            torch.cuda.empty_cache()
        print("[engine] Model unloaded.")

    @property
    def is_loaded(self) -> bool:
        return self._loaded

    def token_count(self, text: str) -> int:
        """Count tokens in a text string (used by Go for context management)."""
        if not self._loaded:
            raise RuntimeError("Model not loaded")
        return len(self.tokenizer.encode(text))


    def _build_prompt(self, messages: list[dict], tools: Optional[list[dict]] = None) -> str:
        """Build chat prompt từ messages bằng Jinja chat template của HF.

        Template (chuỗi prompt) ≠ tokenization — nên vẫn dùng `apply_chat_template`
        của HF (quyết định D1). Token hoá prompt do BPETokenizer tự viết đảm nhiệm.
        """
        # Build messages in OpenAI format for the chat template
        formatted = []
        for msg in messages:
            formatted_msg = {"role": msg["role"], "content": msg.get("content", "")}

            # Handle tool calls from assistant
            if msg.get("tool_calls"):
                formatted_msg["tool_calls"] = [
                    {"id": tc["id"], "type": "function",
                     "function": {"name": tc["name"], "arguments": tc.get("arguments", "{}")}}
                    for tc in msg["tool_calls"]
                ]

            # Handle tool results
            if msg.get("role") == "tool":
                formatted_msg = {
                    "role": "tool",
                    "tool_call_id": msg.get("tool_call_id", ""),
                    "content": msg.get("content", ""),
                }

            formatted.append(formatted_msg)

        # Apply chat template — chỉ dùng HF, không dùng tokenizer tự viết
        try:
            prompt = self.hf_tokenizer.apply_chat_template(
                formatted,
                tools=tools,
                tokenize=False,
                add_generation_prompt=True,
            )
            return prompt
        except Exception:
            # Fallback: format without tools
            prompt = self.hf_tokenizer.apply_chat_template(
                formatted,
                tokenize=False,
                add_generation_prompt=True,
            )
            return prompt

    async def generate(
        self,
        messages: list[dict],
        sampling_params: dict,
        tools: Optional[list[dict]] = None,
        cancel_event=None,  # asyncio.Event for cancellation
    ) -> AsyncIterator[dict]:
        """Generate tokens with streaming.

        Yields dicts: {"type": "token", "token": "..."}
                     | {"type": "tool_use", "id": "...", "name": "...", "arguments": "..."}
                     | {"type": "final", "stop_reason": "...", "usage": {...}}

        Sampling là vòng lặp tự viết trong `worker/sampling.py` (Tuần 5-6) — vẫn
        dùng forward pass + KV cache của HF. Tuần 7-8 sẽ tự quản lý KV cache.
        """
        if not self._loaded:
            raise RuntimeError("Model not loaded. Call load() first.")

        # Build prompt (Jinja template của HF) rồi token hoá bằng tokenizer tự viết
        prompt = self._build_prompt(messages, tools)
        inputs = self.tokenizer.build_inputs([prompt])
        inputs = {k: v.to(DEVICE) for k, v in inputs.items()}

        prompt_tokens = inputs["input_ids"].shape[1]
        params = SamplingParams.from_dict(sampling_params)
        stop_sequences = sampling_params.get("stop_sequences", [])
        stop_ids = {self.tokenizer.eos_token_id, self.tokenizer.pad_token_id}

        # Chạy sampling loop trong thread riêng (forward pass blocking) → đẩy
        # từng token vào queue để async generator tiêu thụ + check cancel.
        out_queue: stdlib_queue.Queue = stdlib_queue.Queue()
        gen_error: list = []
        completion = {"tokens": 0}

        def _run_generation():
            try:
                with torch.no_grad():
                    for row, tid, reason in generate_tokens(
                        self.model,
                        inputs["input_ids"],
                        inputs["attention_mask"],
                        [params],
                        stop_ids,
                        should_stop=lambda: bool(cancel_event and cancel_event.is_set()),
                    ):
                        out_queue.put(("token", tid, reason))
            except Exception as e:  # noqa: BLE001 — chuyển lỗi sang async generator
                gen_error.append(e)
            finally:
                out_queue.put(("end", None, None))

        gen_thread = Thread(target=_run_generation, daemon=True)
        gen_thread.start()

        decoder = StreamingDecoder(self.tokenizer, skip_special_tokens=True)
        generated_text = ""
        stop_reason = None
        finish_reason = None

        try:
            while True:
                if cancel_event and cancel_event.is_set():
                    stop_reason, finish_reason = "STOP_CANCELLED", "cancelled"
                    break

                try:
                    kind, tid, reason = await asyncio.to_thread(out_queue.get, True, 0.2)
                except stdlib_queue.Empty:
                    if gen_error:
                        raise gen_error[0]
                    if not gen_thread.is_alive():
                        break
                    continue

                if kind == "end":
                    break

                if reason == "stop":
                    # eos/pad — không phát ra client, kết thúc lượt
                    if self._contains_tool_call(generated_text):
                        stop_reason, finish_reason = "STOP_TOOL_USE", "tool_use"
                    else:
                        stop_reason, finish_reason = "STOP_END_TURN", "stop"
                    break

                completion["tokens"] += 1
                text = decoder.put(tid)
                candidate = generated_text + text

                # stop_sequences: cắt tại chuỗi dừng, chỉ phát phần text trước nó
                hit = next((s for s in stop_sequences if s in candidate), None)
                if hit:
                    cut = candidate.find(hit)
                    if cut > len(generated_text):
                        yield {"type": "token", "token": candidate[len(generated_text):cut]}
                    stop_reason, finish_reason = "STOP_END_TURN", "stop"
                    break

                generated_text = candidate
                if text:
                    yield {"type": "token", "token": text}

                if reason == "length":
                    stop_reason, finish_reason = "STOP_MAX_TOKENS", "length"
                    break

            gen_thread.join(timeout=1.0)

            if stop_reason is None:
                # Thread kết thúc không có tín hiệu kết thúc (cancel/abnormal)
                if cancel_event and cancel_event.is_set():
                    stop_reason, finish_reason = "STOP_CANCELLED", "cancelled"
                elif self._contains_tool_call(generated_text):
                    stop_reason, finish_reason = "STOP_TOOL_USE", "tool_use"
                else:
                    stop_reason, finish_reason = "STOP_END_TURN", "stop"

            yield self._final_event(
                stop_reason, finish_reason, prompt_tokens, completion["tokens"]
            )

        except Exception:
            yield self._final_event(
                "STOP_ERROR", "error", prompt_tokens, completion["tokens"]
            )

    @staticmethod
    def _final_event(stop_reason: str, finish_reason: str,
                     prompt_tokens: int, completion_tokens: int) -> dict:
        return {
            "type": "final",
            "stop_reason": stop_reason,
            "finish_reason": finish_reason,
            "usage": {
                "prompt_tokens": prompt_tokens,
                "completion_tokens": completion_tokens,
                "total_tokens": prompt_tokens + completion_tokens,
            },
        }

    def _contains_tool_call(self, text: str) -> bool:
        """Detect if generated text contains a tool/function call.

        Llama 3.2 uses special tokens for function calling.
        This is a heuristic — Tuần 3-4 bạn sẽ parse đúng cách khi tự viết tokenizer.
        """
        # Check for common tool call markers
        markers = [
            "<|python_tag|>",
            "<function=",
            "<tool_call>",
            '{"tool_call',
        ]
        return any(marker in text for marker in markers)


# ---------------------------------------------------------------------------
# Singleton
# ---------------------------------------------------------------------------

_engine: Optional[InferenceEngine] = None


def get_engine(model_id: str = MODEL_ID) -> InferenceEngine:
    """Get or create the global inference engine instance."""
    global _engine
    if _engine is None:
        _engine = InferenceEngine(model_id)
    return _engine
