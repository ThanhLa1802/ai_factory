"""Prefix cache cho continuous batching — tái dùng KV của prefix chung.

**Block-aligned**: token ids chia thành block `block_size`; block chỉ được cache
khi đủ. Khoá của block = hash(parent_hash, block_token_ids) — chuỗi hash theo
radix nên block sau chỉ trùng khi **toàn bộ** prefix trước trùng.

`match()` trả các block khớp dài nhất (K/V per-layer) để caller **copy** vào KV
cache của sequence (copy-on-adopt — PagedAttention sẽ thay bằng chia sẻ thật ở
Phase C). `insert()` thêm các block đủ và chưa có sau khi sequence chạy xong.

Bộ nhớ có biên: `max_blocks` + LRU (OrderedDict).
"""

import hashlib
import struct
import threading
from collections import OrderedDict
from typing import List, Optional, Tuple

import torch

DEFAULT_BLOCK_SIZE = 16
DEFAULT_MAX_BLOCKS = 2048


def _block_hash(parent: bytes, toks: Tuple[int, ...]) -> bytes:
    h = hashlib.blake2b(digest_size=16)
    h.update(parent)
    h.update(struct.pack("<I", len(toks)))
    for t in toks:
        h.update(struct.pack("<I", int(t) & 0xFFFFFFFF))
    return h.digest()


class PrefixCache:
    def __init__(self, block_size: int = DEFAULT_BLOCK_SIZE, max_blocks: int = DEFAULT_MAX_BLOCKS):
        self.block_size = max(int(block_size), 1)
        self.max_blocks = max(int(max_blocks), 0)
        # child_hash -> list[(k, v)] per layer, mỗi tensor [1, H, block_size, D]
        self._blocks: "OrderedDict[bytes, list]" = OrderedDict()
        # `match` chạy ở event-loop thread, `insert` ở scheduler thread.
        self._lock = threading.Lock()

    def __len__(self) -> int:
        with self._lock:
            return len(self._blocks)

    def clear(self) -> None:
        with self._lock:
            self._blocks.clear()

    # ------------------------------------------------------------------
    # Lookup / insert
    # ------------------------------------------------------------------

    def match(self, token_ids) -> Tuple[int, Optional[List[Tuple[torch.Tensor, torch.Tensor]]]]:
        """Trả `(matched_len, layers)` cho prefix dài nhất khớp, hoặc `(0, None)`."""
        with self._lock:
            return self._match_locked(token_ids)

    def _match_locked(self, token_ids):
        parent = b""
        matched: list = []
        n = len(token_ids)
        b = 0
        while (b + 1) * self.block_size <= n:
            toks = tuple(token_ids[b * self.block_size : (b + 1) * self.block_size])
            child = _block_hash(parent, toks)
            block = self._blocks.get(child)
            if block is None:
                break
            self._blocks.move_to_end(child)
            matched.append(block)
            parent = child
            b += 1

        if not matched:
            return 0, None

        layers = []
        for layer in range(len(matched[0])):
            k = torch.cat([blk[layer][0] for blk in matched], dim=2)
            v = torch.cat([blk[layer][1] for blk in matched], dim=2)
            layers.append((k, v))
        return b * self.block_size, layers

    def insert(self, token_ids, layers: List[Tuple[torch.Tensor, torch.Tensor]], length: int) -> None:
        """Thêm các block **đủ** chưa có từ `layers` (độ dài hợp lệ `length`)."""
        if not layers:
            return
        with self._lock:
            n = min(int(length), len(token_ids), int(layers[0][0].shape[2]))
            parent = b""
            b = 0
            while (b + 1) * self.block_size <= n:
                toks = tuple(token_ids[b * self.block_size : (b + 1) * self.block_size])
                child = _block_hash(parent, toks)
                if child in self._blocks:
                    self._blocks.move_to_end(child)
                else:
                    block = []
                    for k, v in layers:
                        block.append(
                            (
                                k[:, :, b * self.block_size : (b + 1) * self.block_size, :].clone(),
                                v[:, :, b * self.block_size : (b + 1) * self.block_size, :].clone(),
                            )
                        )
                    self._blocks[child] = block
                    self._evict_locked()
                parent = child
                b += 1

    def _evict_locked(self) -> None:
        while len(self._blocks) > self.max_blocks:
            self._blocks.popitem(last=False)
