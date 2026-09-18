"""Tiny Qwen2 model for CPU-only parity tests of the self-written forward pass.

Dựng một `Qwen2ForCausalLM` nhỏ với random weights (seed cố định) để so logits
giữa forward pass tự viết (`worker/model/forward.py`) và HF — không cần GPU,
không cần weights thật.
"""

import torch
from transformers import Qwen2Config, Qwen2ForCausalLM

TINY_CONFIG = dict(
    vocab_size=128,
    hidden_size=64,
    intermediate_size=128,
    num_hidden_layers=2,
    num_attention_heads=8,
    num_key_value_heads=2,
    max_position_embeddings=256,
    rms_norm_eps=1e-6,
    rope_theta=10000.0,
    hidden_act="silu",
    attention_dropout=0.0,
    use_cache=True,
)


def build_tiny_qwen(seed: int = 0) -> Qwen2ForCausalLM:
    """Tiny Qwen2 trên CPU, eval mode, weights random tái lập theo `seed`."""
    torch.manual_seed(seed)
    config = Qwen2Config(**TINY_CONFIG)
    model = Qwen2ForCausalLM(config)
    model.eval()
    return model


def tiny_config() -> Qwen2Config:
    return Qwen2Config(**TINY_CONFIG)
