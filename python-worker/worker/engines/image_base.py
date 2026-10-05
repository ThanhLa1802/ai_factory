"""ImageBackend — interface tối thiểu cho engine sinh ảnh (swap engine sau interface)."""
import asyncio
from abc import ABC, abstractmethod


class ImageBackend(ABC):
    """Result / progress dict shapes:

      generate(req) -> {
          "png": [bytes, ...], "width": int, "height": int,
          "seed": int, "steps": int, "scheduler": str,
      }

      stream(req) -> async iterator of:
          {"step": int, "total_steps": int, "done": False}
          {"step": int, "total_steps": int, "done": True, "result": {...}}
          {"done": True, "error": str}     # thất bại
    """

    def __init__(self):
        # Cùng vai trò Engine Concurrency Guard của text: một pipeline chạm GPU
        # tại một thời điểm (GenerateImage và StreamImage không chồng nhau).
        self._gen_lock = asyncio.Lock()

    @abstractmethod
    def load(self) -> None:
        """Load pipeline / cấp phát GPU."""

    @abstractmethod
    def unload(self) -> None:
        """Giải phóng pipeline / VRAM."""

    @abstractmethod
    async def generate(self, req: dict) -> dict:
        """Chạy trọn pipeline cho một request, trả ảnh đã encode."""

    @abstractmethod
    def stream(self, req: dict):
        """Trả async iterator tiến độ từng step (kết thúc bằng done)."""
