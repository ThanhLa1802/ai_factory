"""EngineBackend — interface tối thiểu cho engine inference (swap engine sau interface)."""
from abc import ABC, abstractmethod


class EngineBackend(ABC):
    """Event dict shape (generate / generate_batch):
      {"type": "token", "token": str}
      {"type": "tool_use", "id": str, "name": str, "arguments": str}
      {"type": "final", "stop_reason": str, "finish_reason": str, "usage": dict, "token": str|None}
    """

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
