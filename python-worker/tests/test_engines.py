import pytest

from worker.block_manager import BlockManager, BlockPrefixCache, PagedKVCache
from worker.engines import get_backend, TransformersBackend
from worker.engines.transformers import (
    paged_attention_enabled,
    self_forward_enabled,
)


def test_registry_unknown_raises():
    with pytest.raises(ValueError):
        get_backend("nope")


def test_registry_llama():
    b = get_backend("llama", gguf="dummy.gguf", llama_port=8123)
    assert type(b).__name__ == "LlamaBackend"


def test_registry_vllm_spawn():
    b = get_backend("vllm", vllm_port=8123)
    assert type(b).__name__ == "VLLMBackend"
    assert b.server is not None
    assert b.client.base_url == "http://127.0.0.1:8123"


def test_registry_vllm_remote():
    b = get_backend("vllm", vllm_model="served", vllm_url="http://vllm:8000")
    assert type(b).__name__ == "VLLMBackend"
    assert b.server is None
    assert b.model == "served"
    assert b.client.base_url == "http://vllm:8000"


class _StubBatch:
    def __init__(self, events):
        self._events = events
        self.requests = []

    def generate_batch(self, requests):
        self.requests.append(requests)

        async def gen():
            for e in self._events:
                yield e

        return gen()

    def stop(self):
        pass


@pytest.mark.asyncio
async def test_transformers_generate_batch_delegates(monkeypatch):
    events = [
        ("r1", {"type": "token", "token": "hi"}),
        ("r1", {"type": "final", "stop_reason": "STOP_END_TURN", "finish_reason": "stop", "usage": {}}),
    ]
    monkeypatch.setattr("worker.engines.transformers.get_engine", lambda *a, **k: object())
    backend = TransformersBackend()
    backend._batch = _StubBatch(events)
    got = [e async for e in backend.generate_batch([{"request_id": "r1"}])]
    assert got == events


@pytest.mark.asyncio
async def test_transformers_generate_routes_through_scheduler(monkeypatch):
    events = [
        ("", {"type": "token", "token": "hi"}),
        ("", {"type": "final", "stop_reason": "STOP_END_TURN", "finish_reason": "stop", "usage": {}}),
    ]
    monkeypatch.setattr("worker.engines.transformers.get_engine", lambda *a, **k: object())
    backend = TransformersBackend()
    stub = _StubBatch(events)
    backend._batch = stub

    got = [ev async for ev in backend.generate([], {})]
    assert got == [e for _, e in events]
    # single request đi qua scheduler, có mang cancel_event
    assert stub.requests[0][0]["cancel_event"] is None


class _FakeEngine:
    def __init__(self):
        self.model = object()
        self.tokenizer = object()
        self.hf_tokenizer = object()


def _stub_factories(monkeypatch):
    captured = {}

    def fake_forward(model):
        captured["wrapped_model"] = model
        return "FORWARD"

    def fake_hf_adapter(model):
        captured["hf_wrapped_model"] = model
        return "HF_FORWARD"

    def fake_cb(forward, tokenizer, hf_tokenizer, **kwargs):
        captured["forward"] = forward
        captured["kwargs"] = kwargs
        return "BATCH"

    monkeypatch.setattr("worker.engines.transformers.get_engine", lambda *a, **k: _FakeEngine())
    monkeypatch.setattr("worker.engines.transformers.Qwen2Forward", fake_forward)
    monkeypatch.setattr("worker.engines.transformers.HFForwardAdapter", fake_hf_adapter)
    monkeypatch.setattr("worker.engines.transformers.ContinuousBatchEngine", fake_cb)
    return captured


def test_self_forward_enabled_by_default(monkeypatch):
    monkeypatch.delenv("AI_FACTORY_SELF_FORWARD", raising=False)
    assert self_forward_enabled() is True


@pytest.mark.parametrize("value", ["0", "false", "off", "no", "FALSE"])
def test_self_forward_disabled_values(monkeypatch, value):
    monkeypatch.setenv("AI_FACTORY_SELF_FORWARD", value)
    assert self_forward_enabled() is False


def test_get_batch_wraps_self_forward(monkeypatch):
    monkeypatch.delenv("AI_FACTORY_SELF_FORWARD", raising=False)
    captured = _stub_factories(monkeypatch)
    backend = TransformersBackend()
    assert backend._get_batch() == "BATCH"
    assert captured["forward"] == "FORWARD"
    assert captured["wrapped_model"] is backend.engine.model


def test_get_batch_uses_hf_adapter_when_disabled(monkeypatch):
    monkeypatch.setenv("AI_FACTORY_SELF_FORWARD", "0")
    captured = _stub_factories(monkeypatch)
    backend = TransformersBackend()
    assert backend._get_batch() == "BATCH"
    assert captured["forward"] == "HF_FORWARD"
    assert captured["hf_wrapped_model"] is backend.engine.model


def test_paged_attention_disabled_by_default(monkeypatch):
    monkeypatch.delenv("AI_FACTORY_PAGED_ATTENTION", raising=False)
    assert paged_attention_enabled() is False


@pytest.mark.parametrize("value", ["1", "true", "on", "yes"])
def test_paged_attention_enabled_values(monkeypatch, value):
    monkeypatch.setenv("AI_FACTORY_PAGED_ATTENTION", value)
    assert paged_attention_enabled() is True


def test_get_batch_paged_wires_block_manager(monkeypatch):
    from types import SimpleNamespace

    monkeypatch.setenv("AI_FACTORY_PAGED_ATTENTION", "1")
    monkeypatch.setenv("AI_FACTORY_PREFIX_CACHE", "1")
    captured = _stub_factories(monkeypatch)
    backend = TransformersBackend()
    backend.engine.model = SimpleNamespace(
        config=SimpleNamespace(
            num_hidden_layers=2,
            num_key_value_heads=1,
            hidden_size=8,
            num_attention_heads=1,
        )
    )
    assert backend._get_batch() == "BATCH"
    kwargs = captured["kwargs"]
    assert kwargs["paged"] is True
    assert isinstance(kwargs["block_manager"], BlockManager)
    assert isinstance(kwargs["prefix_cache"], BlockPrefixCache)
    assert isinstance(kwargs["cache_factory"](), PagedKVCache)
