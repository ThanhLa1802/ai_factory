"""TransformersBackend — wrap InferenceEngine + BatchEngine hiện có (Qwen2.5-Coder-7B)."""
from ..engine import get_engine
from ..batch_engine import BatchEngine
from .base import EngineBackend


class TransformersBackend(EngineBackend):
    def __init__(self, model_id=None):
        super().__init__()
        self.engine = get_engine(model_id) if model_id else get_engine()
        self._batch = None

    def load(self):
        self.engine.load()

    def unload(self):
        self.engine.unload()

    async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
        async for event in self._guard(self._generate(messages, sampling_params, tools, cancel_event)):
            yield event

    async def _generate(self, messages, sampling_params, tools=None, cancel_event=None):
        async for event in self.engine.generate(
            messages=messages,
            sampling_params=sampling_params,
            tools=tools,
            cancel_event=cancel_event,
        ):
            yield event

    def generate_batch(self, requests):
        # Guarded as one unit: the batch engine never calls self.generate, so the
        # lock is not re-entered.
        return self._guard(self._get_batch().generate_batch(requests))

    def _get_batch(self):
        if self._batch is None:
            self._batch = BatchEngine(
                self.engine.model,
                self.engine.tokenizer,
                self.engine.hf_tokenizer,
            )
        return self._batch
