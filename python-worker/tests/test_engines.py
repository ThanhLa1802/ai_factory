import pytest

from worker.engines import get_backend, TransformersBackend


def test_registry_unknown_raises():
    with pytest.raises(ValueError):
        get_backend("nope")


def test_registry_llama():
    b = get_backend("llama", gguf="dummy.gguf", llama_port=8123)
    assert type(b).__name__ == "LlamaBackend"


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
