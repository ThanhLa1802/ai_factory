"""Tests cho `tests/fake_model.py` — đảm bảo double dùng cache đúng."""

import torch

from fake_model import FakeHFModel


def test_cache_step_matches_full_recompute():
    m = FakeHFModel(vocab=128)

    prompt = torch.tensor([[3, 4, 5]])
    attn = torch.ones(1, 3, dtype=torch.long)
    out = m(prompt, attn, past_key_values=None)
    t0 = int(out.logits[0, -1, :].argmax().item())

    # decode 1 bước dùng cache
    out2 = m(
        torch.tensor([[t0]]),
        torch.ones(1, 4, dtype=torch.long),
        past_key_values=out.past_key_values,
    )
    t1_cached = int(out2.logits[0, -1, :].argmax().item())

    # tham chiếu: chạy lại full sequence không cache
    ref = m(torch.tensor([[3, 4, 5, t0]]), torch.ones(1, 4, dtype=torch.long), past_key_values=None)
    t1_ref = int(ref.logits[0, -1, :].argmax().item())

    assert t1_cached == t1_ref


def test_cache_detects_missing_history():
    m = FakeHFModel(vocab=128)
    out = m(torch.tensor([[3, 4, 5]]), torch.ones(1, 3, dtype=torch.long), past_key_values=None)
    t0 = int(out.logits[0, -1, :].argmax().item())

    correct = m(
        torch.tensor([[t0]]),
        torch.ones(1, 4, dtype=torch.long),
        past_key_values=out.past_key_values,
    ).logits[0, -1, :].argmax().item()

    # "quên" token đầu (giả lập assembly sai) → phải khác
    wrong_mask = torch.tensor([[0, 1, 1, 1]])
    missing = m(
        torch.tensor([[t0]]),
        wrong_mask,
        past_key_values=out.past_key_values,
    ).logits[0, -1, :].argmax().item()

    assert correct != missing
