"""TransformersBackend — wrap InferenceEngine + ContinuousBatchEngine (Qwen2.5-Coder-7B).

Batch path dùng `ContinuousBatchEngine` (KV cache tự quản + continuous batching).
Single `generate` cũng đi qua scheduler để chỉ một luồng sở hữu model (D4), nên
không cần `_gen_lock` bọc-trọn-batch nữa.
"""
from ..continuous_batch_engine import ContinuousBatchEngine
from ..engine import DEVICE, get_engine
from .base import EngineBackend


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
            self._batch = ContinuousBatchEngine(
                self.engine.model,
                self.engine.tokenizer,
                self.engine.hf_tokenizer,
                device=DEVICE,
            )
        return self._batch
