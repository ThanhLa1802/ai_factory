"""TransformersBackend — wrap InferenceEngine + ContinuousBatchEngine (Qwen2.5-Coder-7B).

Batch path dùng `ContinuousBatchEngine` (KV cache tự quản + continuous batching).
Single `generate` cũng đi qua scheduler để chỉ một luồng sở hữu model (D4), nên
không cần `_gen_lock` bọc-trọn-batch nữa.

Forward pass mặc định là **tự viết** (`Qwen2Forward`, Tuần 9+); đặt
`AI_FACTORY_SELF_FORWARD=0` để quay về HF (rollback — D7).
"""
import os

from ..block_manager import BlockManager, BlockPrefixCache, PagedKVCache
from ..continuous_batch_engine import ContinuousBatchEngine
from ..engine import DEVICE, get_engine
from ..model.forward import Qwen2Forward
from ..model.hf_forward import HFForwardAdapter
from ..prefix_cache import PrefixCache
from .base import EngineBackend

_FALSE_VALUES = {"0", "false", "off", "no"}


def self_forward_enabled() -> bool:
    """Đọc cờ `AI_FACTORY_SELF_FORWARD` (default: bật)."""
    return os.environ.get("AI_FACTORY_SELF_FORWARD", "1").strip().lower() not in _FALSE_VALUES


def prefix_cache_enabled() -> bool:
    """Đọc cờ `AI_FACTORY_PREFIX_CACHE` (default: bật)."""
    return os.environ.get("AI_FACTORY_PREFIX_CACHE", "1").strip().lower() not in _FALSE_VALUES


def paged_attention_enabled() -> bool:
    """Đọc cờ `AI_FACTORY_PAGED_ATTENTION` (default: tắt — chờ đo trên GPU)."""
    return os.environ.get("AI_FACTORY_PAGED_ATTENTION", "0").strip().lower() not in _FALSE_VALUES


def build_prefix_cache() -> PrefixCache:
    return PrefixCache(
        block_size=int(os.environ.get("AI_FACTORY_PREFIX_CACHE_BLOCK_SIZE", "16")),
        max_blocks=int(os.environ.get("AI_FACTORY_PREFIX_CACHE_BLOCKS", "2048")),
    )


def build_block_manager(model) -> BlockManager:
    cfg = model.config
    block_size = int(
        os.environ.get(
            "AI_FACTORY_PAGED_BLOCK_SIZE",
            os.environ.get("AI_FACTORY_PREFIX_CACHE_BLOCK_SIZE", "16"),
        )
    )
    return BlockManager(
        num_blocks=int(os.environ.get("AI_FACTORY_PAGED_BLOCKS", "2048")),
        num_layers=int(cfg.num_hidden_layers),
        num_kv_heads=int(cfg.num_key_value_heads),
        block_size=block_size,
        head_dim=int(cfg.hidden_size // cfg.num_attention_heads),
    )


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
            prefix_cache = build_prefix_cache() if prefix_cache_enabled() else None
            paged = paged_attention_enabled()
            block_manager = None
            factory = None
            if paged:
                block_manager = build_block_manager(self.engine.model)
                if prefix_cache is not None:
                    prefix_cache = BlockPrefixCache(
                        block_manager, block_size=block_manager.block_size
                    )
                factory = lambda: PagedKVCache(block_manager)
            self._batch = ContinuousBatchEngine(
                forward,
                self.engine.tokenizer,
                self.engine.hf_tokenizer,
                device=DEVICE,
                prefix_cache=prefix_cache,
                paged=paged,
                block_manager=block_manager,
                cache_factory=factory,
            )
        return self._batch
