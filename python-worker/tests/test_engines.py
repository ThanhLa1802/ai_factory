import pytest

from worker.engines import get_backend, TransformersBackend


def test_registry_unknown_raises():
    with pytest.raises(ValueError):
        get_backend("nope")


def test_registry_llama():
    b = get_backend("llama", gguf="dummy.gguf", llama_port=8123)
    assert type(b).__name__ == "LlamaBackend"


@pytest.mark.asyncio
async def test_transformers_generate_delegates(monkeypatch):
    events = [
        {"type": "token", "token": "hi"},
        {"type": "final", "stop_reason": "STOP_END_TURN",
         "finish_reason": "stop", "usage": {}},
    ]

    class StubEngine:
        async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
            for ev in events:
                yield ev

    monkeypatch.setattr("worker.engines.transformers.get_engine",
                        lambda *a, **k: StubEngine())
    backend = TransformersBackend()
    got = [ev async for ev in backend.generate([], {})]
    assert got == events
