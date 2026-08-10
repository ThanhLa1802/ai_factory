"""
Inference engine — wraps HuggingFace model for token generation.

Uses Qwen 2.5 3B Instruct with 4-bit quantization (bitsandbytes).
Tokenizer là BPETokenizer tự viết (byte-level BPE); HF `AutoTokenizer` chỉ được
giữ để build chat template (apply_chat_template) — quyết định D1 trong spec.
Handles: tokenization, forward pass, sampling, KV cache (via HF).
"""

import time
from typing import AsyncIterator, Optional

import torch
from transformers import (
    AutoModelForCausalLM,
    AutoTokenizer,
    BitsAndBytesConfig,
    TextStreamer,
    TextIteratorStreamer,
)
from threading import Thread

from .model.tokenizer import BPETokenizer

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

        In the future (Tuần 5-6), you'll replace the sampling logic here.
        In the future (Tuần 7-8), you'll manage KV cache manually instead of using model.generate().
        """
        if not self._loaded:
            raise RuntimeError("Model not loaded. Call load() first.")

        # Build prompt (Jinja template của HF) rồi token hoá bằng tokenizer tự viết
        prompt = self._build_prompt(messages, tools)
        inputs = self.tokenizer.build_inputs([prompt])
        inputs = {k: v.to(DEVICE) for k, v in inputs.items()}

        prompt_tokens = inputs["input_ids"].shape[1]
        max_new = sampling_params.get("max_tokens", 1024)
        temperature = sampling_params.get("temperature", 0.7)
        top_p = sampling_params.get("top_p", 0.9)
        top_k = sampling_params.get("top_k", 50)
        stop_sequences = sampling_params.get("stop_sequences", [])

        # Setup streamer for per-token output
        streamer = TextIteratorStreamer(
            self.tokenizer,
            skip_prompt=True,
            skip_special_tokens=True,
        )

        # Generation kwargs — Trong tương lai, đây là nơi bạn sẽ thay bằng
        # manual sampling loop (Tuần 5-6) và manual KV cache (Tuần 7-8).
        gen_kwargs = {
            "input_ids": inputs["input_ids"],
            "attention_mask": inputs["attention_mask"],
            "max_new_tokens": max_new,
            "temperature": temperature if temperature > 0 else 1.0,
            "top_p": top_p,
            "top_k": top_k,
            "do_sample": temperature > 0,
            "pad_token_id": self.tokenizer.pad_token_id,
            "eos_token_id": self.tokenizer.eos_token_id,
            "streamer": streamer,
        }

        if stop_sequences:
            gen_kwargs["stop_strings"] = stop_sequences

        # Run generation in a separate thread so we can check cancellation
        completion_tokens = 0
        generated_text = ""

        def _run_generation():
            nonlocal completion_tokens, generated_text
            result = self.model.generate(**gen_kwargs)
            # Count new tokens
            completion_tokens = result.shape[1] - prompt_tokens

        gen_thread = Thread(target=_run_generation, daemon=True)
        gen_thread.start()

        # Stream tokens one at a time
        try:
            for token in streamer:
                # Check cancellation between tokens
                if cancel_event and cancel_event.is_set():
                    # Force stop: trong tương lai (Tuần 7-8) bạn sẽ chủ động
                    # giải phóng KV cache ở đây thay vì chỉ bỏ qua thread
                    yield {
                        "type": "final",
                        "stop_reason": "STOP_CANCELLED",
                        "finish_reason": "cancelled",
                        "usage": {
                            "prompt_tokens": prompt_tokens,
                            "completion_tokens": completion_tokens,
                            "total_tokens": prompt_tokens + completion_tokens,
                        },
                    }
                    return

                generated_text += token
                yield {"type": "token", "token": token}

            # Generation hoàn tất — đợi thread join
            gen_thread.join()

            # Detect finish reason
            if completion_tokens >= max_new:
                stop_reason = "STOP_MAX_TOKENS"
                finish_reason = "length"
            else:
                # Check if model output contains a tool call
                # Llama 3.2 format: <|python_tag|>function_name\n{...}
                # Trong tương lai bạn sẽ tự parse cái này khi thay tokenizer
                if self._contains_tool_call(generated_text):
                    stop_reason = "STOP_TOOL_USE"
                    finish_reason = "tool_use"
                else:
                    stop_reason = "STOP_END_TURN"
                    finish_reason = "stop"

            yield {
                "type": "final",
                "stop_reason": stop_reason,
                "finish_reason": finish_reason,
                "usage": {
                    "prompt_tokens": prompt_tokens,
                    "completion_tokens": completion_tokens,
                    "total_tokens": prompt_tokens + completion_tokens,
                },
            }

        except Exception as e:
            yield {
                "type": "final",
                "stop_reason": "STOP_ERROR",
                "finish_reason": "error",
                "usage": {
                    "prompt_tokens": prompt_tokens,
                    "completion_tokens": 0,
                    "total_tokens": prompt_tokens,
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
