"""TransformersBackend — wrap InferenceEngine + ContinuousBatchEngine (Qwen2.5-Coder-7B).

Batch path dùng `ContinuousBatchEngine` (KV cache tự quản + continuous batching).
Single `generate` cũng đi qua scheduler để chỉ một luồng sở hữu model (D4), nên
không cần `_gen_lock` bọc-trọn-batch nữa.

Forward pass mặc định là **tự viết** (`Qwen2Forward`, Tuần 9+); đặt
`AI_FACTORY_SELF_FORWARD=0` để quay về HF (rollback — D7).
"""
import os

from ..continuous_batch_engine import ContinuousBatchEngine
from ..engine import DEVICE, get_engine
from ..model.forward import Qwen2Forward
from ..model.hf_forward import HFForwardAdapter
from .base import EngineBackend

_FALSE_VALUES = {"0", "false", "off", "no"}


def self_forward_enabled() -> bool:
    """Đọc cờ `AI_FACTORY_SELF_FORWARD` (default: bật)."""
    return os.environ.get("AI_FACTORY_SELF_FORWARD", "1").strip().lower() not in _FALSE_VALUES


class TransformersBackend(EngineBackend):
    def __init__(self, model_id=None):
        super().__init__()
        self.engine = get_engine(model_id) if model_id else get_engine()
        self._batch = None

    def load(self):
        self.engine.load()

    def unload(self):
        if self._batch is not None:
            self._batch.stop()
            self._batch = None
        self.engine.unload()

    async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
        # Route single request qua scheduler (một luồng điều khiển model).
        request = {
            "request_id": "",
            "messages": messages,
            "sampling_params": sampling_params,
            "tools": tools,
            "cancel_event": cancel_event,
        }
        async for _rid, event in self._get_batch().generate_batch([request]):
            yield event

    def generate_batch(self, requests):
        return self._get_batch().generate_batch(requests)

    def _get_batch(self):
        if self._batch is None:
            forward = (
                Qwen2Forward(self.engine.model)
                if self_forward_enabled()
                else HFForwardAdapter(self.engine.model)
            )
            self._batch = ContinuousBatchEngine(
                forward,
                self.engine.tokenizer,
                self.engine.hf_tokenizer,
                device=DEVICE,
            )
        return self._batch
