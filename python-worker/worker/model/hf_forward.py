"""Adapter cho đường HF fallback (`AI_FACTORY_SELF_FORWARD=0`).

Từ transformers 4.47, `Qwen2Model.forward` chỉ nhận `past_key_values` là `Cache`,
không nhận legacy tuple — trong khi `ContinuousBatchEngine`/`KVCache` làm việc với
tuple. Adapter này chuyển tuple → `DynamicCache` khi vào và `Cache` → tuple khi ra,
để có thể rollback về forward pass của HF mà không phải sửa scheduler.
"""

import torch
from transformers.cache_utils import DynamicCache

from .forward import ForwardOutput


class HFForwardAdapter:
    def __init__(self, hf_model):
        self.model = hf_model

    def __call__(
        self,
        input_ids: torch.Tensor,
        attention_mask=None,
        position_ids=None,
        past_key_values=None,
        use_cache: bool = True,
        **kwargs,
    ) -> ForwardOutput:
        cache = None
        if past_key_values is not None:
            cache = (
                past_key_values
                if hasattr(past_key_values, "get_seq_length")
                else DynamicCache.from_legacy_cache(past_key_values)
            )

        out = self.model(
            input_ids=input_ids,
            attention_mask=attention_mask,
            position_ids=position_ids,
            past_key_values=cache,
            use_cache=use_cache,
            **kwargs,
        )

        past = out.past_key_values
        if past is not None and hasattr(past, "to_legacy_cache"):
            past = past.to_legacy_cache()
        return ForwardOutput(out.logits, past)
