"""VisionBackend — Qwen2.5-VL (image-text-to-text) cho engine `--engine vl`.

Khác với `TransformersBackend` (text-only, continuous batching + forward pass tự
viết), VLM cần processor riêng để biến ảnh thành `pixel_values` và một chat
template có placeholder ảnh. Vì vậy backend này chạy thẳng `model.generate()`
của transformers và stream text qua `TextIteratorStreamer`.

Ràng buộc:
- Mỗi lúc chỉ một engine sở hữu GPU (giống llama/SDXL). Chạy worker text và
  worker VL đồng thời trên 12GB sẽ OOM.
- Batch được xử lý tuần tự: mỗi request có thể có số ảnh/kích thước khác nhau,
  gộp chung một forward pass sẽ phải pad rất nhiều.
- Tool calling chưa hỗ trợ trên engine này (tools bị bỏ qua).
"""
import asyncio
import base64
import io
import os
import threading

from .base import EngineBackend

DEFAULT_MODEL = "Qwen/Qwen2.5-VL-7B-Instruct"
_FALSE_VALUES = {"0", "false", "off", "no"}


def _env_int(name: str, default: int) -> int:
    try:
        return int(os.environ.get(name, str(default)))
    except ValueError:
        return default


def decode_images(images) -> list:
    """Decode danh sách ảnh (base64 str hoặc raw bytes) thành PIL RGB images."""
    from PIL import Image

    out = []
    for raw in images or []:
        if isinstance(raw, str):
            raw = base64.b64decode(raw)
        img = Image.open(io.BytesIO(raw))
        out.append(img.convert("RGB"))
    return out


def build_hf_messages(messages) -> tuple[list[dict], list]:
    """Map internal canonical messages → Qwen chat-template messages + PIL images.

    Internal message shape: {"role", "content", "images"?, "tool_calls"?,
    "tool_result"?}. Images are attached to the user turn and must appear before
    the text part in the Qwen template.
    """
    hf_messages: list[dict] = []
    pil_images: list = []

    for m in messages:
        role = m.get("role", "user")
        text = m.get("content") or ""
        images = decode_images(m.get("images"))

        if role == "system":
            if text:
                hf_messages.append({"role": "system", "content": text})
            continue

        if role == "tool":
            result = m.get("tool_result") or text
            hf_messages.append({"role": "user", "content": f"Tool result:\n{result}"})
            continue

        if role == "assistant":
            hf_messages.append({"role": "assistant", "content": text})
            continue

        parts = [{"type": "image", "image": img} for img in images]
        if text:
            parts.append({"type": "text", "text": text})
        if not parts:
            parts.append({"type": "text", "text": ""})
        pil_images.extend(images)
        hf_messages.append({"role": "user", "content": parts})

    return hf_messages, pil_images


def sampling_kwargs(params) -> dict:
    """Translate SamplingParams dict → HF generate kwargs (greedy when temp<=0)."""
    max_new = int(params.get("max_tokens") or 1024)
    temperature = float(params.get("temperature") or 0.0)
    kwargs = {"max_new_tokens": max(1, max_new)}
    if temperature <= 0:
        kwargs["do_sample"] = False
        return kwargs
    kwargs.update(
        do_sample=True,
        temperature=temperature,
        top_p=float(params.get("top_p") or 0.9),
        top_k=int(params.get("top_k") or 50),
    )
    return kwargs


class _CancelCriteria:
    """HF stopping criteria: stop the blocking generate when the gRPC request dies."""

    def __init__(self, cancel_event):
        self.cancel_event = cancel_event

    def __call__(self, input_ids, scores, **kwargs):  # noqa: ARG002
        return bool(self.cancel_event is not None and self.cancel_event.is_set())


class VisionBackend(EngineBackend):
    supports_vision = True

    def __init__(self, model_id=None):
        super().__init__()
        self.model_id = model_id or os.environ.get("AI_FACTORY_VL_MODEL", DEFAULT_MODEL)
        self.quant = os.environ.get("AI_FACTORY_VL_QUANT", "4bit").strip().lower()
        self.min_pixels = _env_int("AI_FACTORY_VL_MIN_PIXELS", 256 * 28 * 28)
        self.max_pixels = _env_int("AI_FACTORY_VL_MAX_PIXELS", 1280 * 28 * 28)
        self.model = None
        self.processor = None

    def load(self):
        import torch
        from transformers import (
            AutoProcessor,
            BitsAndBytesConfig,
            Qwen2_5_VLForConditionalGeneration,
            TextIteratorStreamer,
        )

        quant_config = None
        use_4bit = self.quant == "4bit" and torch.cuda.is_available()
        if use_4bit:
            quant_config = BitsAndBytesConfig(
                load_in_4bit=True,
                bnb_4bit_quant_type="nf4",
                bnb_4bit_compute_dtype=torch.bfloat16,
                bnb_4bit_use_double_quant=True,
                # Giữ vision tower ở bf16: lượng tử hoá vision encoder làm giảm
                # chất lượng đọc ảnh rõ rệt mà chỉ tiết kiệm ~1GB.
                llm_int8_skip_modules=["visual"],
            )

        kwargs = {
            "torch_dtype": torch.bfloat16,
            "device_map": "auto" if torch.cuda.is_available() else "cpu",
            "attn_implementation": "sdpa",
        }
        if quant_config is not None:
            kwargs["quantization_config"] = quant_config

        self.model = Qwen2_5_VLForConditionalGeneration.from_pretrained(
            self.model_id, **kwargs
        )
        self.processor = AutoProcessor.from_pretrained(
            self.model_id,
            min_pixels=self.min_pixels,
            max_pixels=self.max_pixels,
        )
        self._TextIteratorStreamer = TextIteratorStreamer
        self.model.eval()
        print(
            f"[vision] loaded {self.model_id} "
            f"(quant={'4bit' if use_4bit else 'none'}, "
            f"max_pixels={self.max_pixels})"
        )

    def unload(self):
        self.model = None
        self.processor = None
        try:
            import torch

            if torch.cuda.is_available():
                torch.cuda.empty_cache()
        except Exception:  # noqa: BLE001 — best-effort cleanup
            pass

    async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
        request = {
            "request_id": "",
            "messages": messages,
            "sampling_params": sampling_params,
            "cancel_event": cancel_event,
        }
        async for _rid, event in self.generate_batch([request]):
            yield event

    async def generate_batch(self, requests):
        async with self._gen_lock:
            for req in requests:
                async for event in self._generate_one(req):
                    yield req.get("request_id", ""), event

    async def _generate_one(self, request):
        """Run one multimodal generation, yielding engine events."""
        import torch

        if self.model is None or self.processor is None:
            yield {
                "type": "final",
                "stop_reason": "STOP_ERROR",
                "finish_reason": "error",
                "token": "vision model is not loaded",
            }
            return

        messages = request.get("messages") or []
        params = request.get("sampling_params") or {}
        cancel_event = request.get("cancel_event")

        try:
            hf_messages, images = build_hf_messages(messages)
            prompt = self.processor.apply_chat_template(
                hf_messages, tokenize=False, add_generation_prompt=True
            )
            inputs = self.processor(
                text=[prompt],
                images=images or None,
                return_tensors="pt",
            )
            inputs = inputs.to(self.model.device)
            prompt_tokens = int(inputs["input_ids"].shape[1])
        except Exception as e:  # noqa: BLE001
            yield {
                "type": "final",
                "stop_reason": "STOP_ERROR",
                "finish_reason": "error",
                "token": f"prepare image input: {e}",
            }
            return

        streamer = self._TextIteratorStreamer(
            self.processor.tokenizer, skip_prompt=True, skip_special_tokens=True
        )
        gen_kwargs = dict(**inputs, streamer=streamer, stopping_criteria=[_CancelCriteria(cancel_event)])
        gen_kwargs.update(sampling_kwargs(params))

        error_box: dict = {}

        def _run_generate():
            try:
                with torch.inference_mode():
                    self.model.generate(**gen_kwargs)
            except Exception as e:  # noqa: BLE001
                error_box["error"] = str(e)
            finally:
                streamer.end()

        loop = asyncio.get_running_loop()
        queue: asyncio.Queue = asyncio.Queue()

        def _pump():
            try:
                for text in streamer:
                    if text:
                        loop.call_soon_threadsafe(queue.put_nowait, ("token", text))
            except Exception as e:  # noqa: BLE001
                loop.call_soon_threadsafe(queue.put_nowait, ("error", str(e)))
            finally:
                loop.call_soon_threadsafe(queue.put_nowait, ("done", None))

        threading.Thread(target=_run_generate, daemon=True).start()
        threading.Thread(target=_pump, daemon=True).start()

        text_parts: list[str] = []
        cancelled = False
        while True:
            kind, payload = await queue.get()
            if kind == "done":
                break
            if kind == "error":
                error_box.setdefault("error", payload)
                break
            if cancel_event is not None and cancel_event.is_set():
                cancelled = True
                break
            text_parts.append(payload)
            yield {"type": "token", "token": payload}

        text = "".join(text_parts)
        completion_tokens = len(
            self.processor.tokenizer(text, add_special_tokens=False)["input_ids"]
        )
        max_new = int(params.get("max_tokens") or 1024)

        if error_box.get("error"):
            yield {
                "type": "final",
                "stop_reason": "STOP_ERROR",
                "finish_reason": "error",
                "token": error_box["error"],
            }
            return

        if cancelled:
            stop_reason, finish_reason = "STOP_CANCELLED", "cancelled"
        elif completion_tokens >= max_new:
            stop_reason, finish_reason = "STOP_MAX_TOKENS", "length"
        else:
            stop_reason, finish_reason = "STOP_END_TURN", "stop"

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
