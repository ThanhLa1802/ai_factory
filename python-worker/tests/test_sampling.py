"""Tests cho sampling loop tự viết (`worker/sampling.py`).

Toàn bộ chạy trên CPU, không cần model thật:
- Toán sampling: temperature / top-k / top-p / greedy / multinomial.
- Vòng lặp autoregressive: dùng model giả để kiểm tra stop (eos/max), KV cache
  được truyền giữa các bước, per-row params, và cancel qua should_stop.
"""

import torch

from worker.sampling import (
    SamplingParams,
    apply_temperature,
    apply_top_k,
    apply_top_p,
    generate_tokens,
    sample_next,
)


# ---------------------------------------------------------------------------
# SamplingParams
# ---------------------------------------------------------------------------

def test_sampling_params_from_dict_defaults():
    p = SamplingParams.from_dict(None)
    assert (p.max_tokens, p.temperature, p.top_k, p.top_p) == (1024, 0.7, 50, 0.9)
    p = SamplingParams.from_dict({"max_tokens": 8, "temperature": 0.0, "top_k": 0, "top_p": 1.0})
    assert (p.max_tokens, p.temperature, p.top_k, p.top_p) == (8, 0.0, 0, 1.0)


# ---------------------------------------------------------------------------
# Logits transforms
# ---------------------------------------------------------------------------

def test_apply_temperature_scales():
    logits = torch.tensor([[2.0, 4.0]])
    assert torch.allclose(apply_temperature(logits, 2.0), torch.tensor([[1.0, 2.0]]))
    # temperature <= 0 → để nguyên (greedy lo)
    assert torch.equal(apply_temperature(logits, 0.0), logits)


def test_apply_top_k_keeps_k():
    logits = torch.tensor([[5.0, 4.0, 3.0, 2.0, 1.0]])
    out = apply_top_k(logits, 2)
    finite = torch.isfinite(out[0])
    assert finite.sum().item() == 2
    assert finite[0] and finite[1] and not finite[2]


def test_apply_top_k_disabled():
    logits = torch.tensor([[5.0, 4.0, 3.0]])
    assert torch.equal(apply_top_k(logits, 0), logits)
    assert torch.equal(apply_top_k(logits, 10), logits)


def test_apply_top_p_nucleus():
    # probs [0.644, 0.237, 0.087, 0.032] → top_p=0.8 giữ đúng 2 token cao nhất
    logits = torch.tensor([[3.0, 2.0, 1.0, 0.0]])
    out = apply_top_p(logits, 0.8)
    finite = torch.isfinite(out[0])
    assert finite.sum().item() == 2
    assert finite[0] and finite[1]


def test_apply_top_p_zero_keeps_argmax():
    logits = torch.tensor([[1.0, 9.0, 3.0]])
    out = apply_top_p(logits, 0.0)
    assert torch.isfinite(out[0]).sum().item() == 1
    assert torch.isfinite(out[0, 1])


def test_apply_top_p_one_is_noop():
    logits = torch.tensor([[1.0, 2.0, 3.0]])
    assert torch.equal(apply_top_p(logits, 1.0), logits)


# ---------------------------------------------------------------------------
# sample_next
# ---------------------------------------------------------------------------

def test_sample_next_greedy_matches_argmax():
    logits = torch.tensor([[0.5, 7.0, 2.0]])
    params = SamplingParams(temperature=0.0, max_tokens=1)
    assert sample_next(logits, params).item() == 1


def test_sample_next_seeded_reproducible():
    logits = torch.tensor([[1.0, 1.0, 1.0, 1.0]])
    params = SamplingParams(temperature=1.0, top_k=0, top_p=1.0, max_tokens=1)
    g1 = torch.Generator().manual_seed(123)
    g2 = torch.Generator().manual_seed(123)
    a = [sample_next(logits, params, g1).item() for _ in range(20)]
    b = [sample_next(logits, params, g2).item() for _ in range(20)]
    assert a == b


def test_sample_next_distribution_matches_softmax():
    logits = torch.tensor([[2.0, 1.0, 0.0]])
    params = SamplingParams(temperature=1.0, top_k=0, top_p=1.0, max_tokens=1)
    expected = torch.softmax(logits, dim=-1)[0]

    gen = torch.Generator().manual_seed(7)
    n = 20000
    counts = torch.zeros(3)
    for _ in range(n):
        counts[sample_next(logits, params, gen).item()] += 1
    freq = counts / n
    assert torch.allclose(freq, expected, atol=0.03)


def test_sample_next_top_k_masks_low_prob():
    logits = torch.tensor([[10.0, 0.0, 0.0]])
    params = SamplingParams(temperature=1.0, top_k=1, top_p=1.0, max_tokens=1)
    gen = torch.Generator().manual_seed(1)
    assert all(sample_next(logits, params, gen).item() == 0 for _ in range(20))


# ---------------------------------------------------------------------------
# generate_tokens (model giả, greedy)
# ---------------------------------------------------------------------------

class _FakeOutput:
    def __init__(self, logits, past):
        self.logits = logits
        self.past_key_values = past


class _FakeModel:
    """Phát lần lượt token trong `plan[row]` — greedy nên kết quả tất định."""

    def __init__(self, plan, vocab=16):
        self.plan = plan
        self.vocab = vocab
        self.step = 0
        self.pasts = []
        self.attn_lens = []

    def __call__(self, input_ids, attention_mask, past_key_values=None, use_cache=True):
        self.pasts.append(past_key_values)
        self.attn_lens.append(attention_mask.shape[1])
        logits = torch.full((input_ids.shape[0], 1, self.vocab), -20.0)
        for row in range(input_ids.shape[0]):
            seq = self.plan[row]
            tid = seq[min(self.step, len(seq) - 1)]
            logits[row, 0, tid] = 20.0
        self.step += 1
        return _FakeOutput(logits, "CACHE")


def _greedy(max_tokens=10):
    return SamplingParams(temperature=0.0, top_k=0, top_p=1.0, max_tokens=max_tokens)


def _inputs(batch=1, length=3):
    return (
        torch.zeros((batch, length), dtype=torch.long),
        torch.ones((batch, length), dtype=torch.long),
    )


def test_generate_tokens_stops_on_eos():
    model = _FakeModel(plan=[[1, 2, 9]])
    ids, attn = _inputs()
    events = list(generate_tokens(model, ids, attn, [_greedy()], stop_ids={9}))
    assert events == [(0, 1, None), (0, 2, None), (0, 9, "stop")]


def test_generate_tokens_stops_on_max():
    model = _FakeModel(plan=[[1, 2, 3]])
    ids, attn = _inputs()
    events = list(generate_tokens(model, ids, attn, [_greedy(max_tokens=2)], stop_ids={9}))
    assert events == [(0, 1, None), (0, 2, "length")]


def test_generate_tokens_forwards_kv_cache_and_grows_mask():
    model = _FakeModel(plan=[[1, 2, 3]])
    ids, attn = _inputs(length=3)
    list(generate_tokens(model, ids, attn, [_greedy(max_tokens=3)], stop_ids={9}))
    # Bước 1: chưa có cache; các bước sau dùng lại cache của bước trước.
    assert model.pasts[0] is None
    assert model.pasts[1] == "CACHE" and model.pasts[2] == "CACHE"
    assert model.attn_lens == [3, 4, 5]


def test_generate_tokens_per_row_max():
    model = _FakeModel(plan=[[1, 2, 3], [4, 5, 6]])
    ids, attn = _inputs(batch=2)
    params_list = [_greedy(max_tokens=1), _greedy(max_tokens=3)]
    events = list(generate_tokens(model, ids, attn, params_list, stop_ids={9}))
    assert events == [
        (0, 1, "length"),
        (1, 4, None),
        (1, 5, None),
        (1, 6, "length"),
    ]


def test_generate_tokens_zero_max_is_noop():
    model = _FakeModel(plan=[[1, 2, 3]])
    ids, attn = _inputs()
    events = list(generate_tokens(model, ids, attn, [_greedy(max_tokens=0)], stop_ids={9}))
    assert events == []
    assert model.step == 0


def test_generate_tokens_should_stop():
    model = _FakeModel(plan=[[1, 2, 3]])
    ids, attn = _inputs()
    events = list(
        generate_tokens(model, ids, attn, [_greedy()], stop_ids={9}, should_stop=lambda: True)
    )
    assert events == []
    assert model.step == 0
