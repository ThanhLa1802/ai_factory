"""EngineBackend — interface tối thiểu cho engine inference (swap engine sau interface)."""
import asyncio
from abc import ABC, abstractmethod


class EngineBackend(ABC):
    """Event dict shape (generate / generate_batch):
      {"type": "token", "token": str}
      {"type": "tool_use", "id": str, "name": str, "arguments": str}
      {"type": "final", "stop_reason": str, "finish_reason": str, "usage": dict, "token": str|None}
    """

    def __init__(self):
        # Engine Concurrency Guard: serialize access to the shared model so the
        # single Generate and BatchGenerate paths can never drive it at once.
        # The Go Batch Slot shapes throughput; this is the safety net.
        self._gen_lock = asyncio.Lock()

    async def _guard(self, events):
        """Yield events from `events` while holding the backend's guard."""
        async with self._gen_lock:
            async for ev in events:
                yield ev

    @abstractmethod
    def load(self) -> None:
        """Load model / spawn engine subprocess."""

    @abstractmethod
    def unload(self) -> None:
        """Giải phóng model / dừng subprocess."""

    @abstractmethod
    async def generate(self, messages, sampling_params, tools=None, cancel_event=None):
        """Stream events cho 1 request. messages đã gồm system prompt (nếu có)."""

    @abstractmethod
    def generate_batch(self, requests):
        """Trả async iterator (request_id, event). requests: list[dict] (system_prompt là key riêng)."""
