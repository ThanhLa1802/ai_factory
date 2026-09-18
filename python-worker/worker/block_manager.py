"""PagedAttention (bản PyTorch) — pool block vật lý + block table per-sequence.

Thay buffer liền mạch (`KVCache`) bằng block cố định:

- `BlockManager`     — pool per-layer `[num_blocks, H_kv, block_size, D]` + refcount + LRU.
- `PagedKVCache`     — block table per-sequence + length; gather/append/adopt/fork/CoW.
- `BlockPrefixCache` — `hash(parent, block_tokens) -> block_id`, chia sẻ block prefix thật.

Attention vẫn chạy `gqa_attention`; khác biệt là K/V được đọc từ block theo block
table (**PyTorch gather — KHÔNG CUDA kernel**; chấp nhận chậm hơn vLLM, mục tiêu là
học cơ chế quản lý bộ nhớ + đo lường).
"""

import hashlib
import struct
import threading
from collections import OrderedDict
from typing import List, Optional, Tuple

import torch

DEFAULT_BLOCK_SIZE = 16


class OutOfBlocks(RuntimeError):
    """Pool block hết (mọi block đều đang được tham chiếu)."""


def _block_hash(parent: bytes, toks: Tuple[int, ...]) -> bytes:
    h = hashlib.blake2b(digest_size=16)
    h.update(parent)
    h.update(struct.pack("<I", len(toks)))
    for t in toks:
        h.update(struct.pack("<I", int(t) & 0xFFFFFFFF))
    return h.digest()


class BlockManager:
    """Pool block vật lý dùng chung cho mọi sequence.

    Block có `refcount == 0` nằm trong **free LRU** và có thể bị tái dùng; tái dùng
    gọi `on_evict(bid)` để lớp prefix cache xoá mapping cũ (best-effort).
    """

    def __init__(self, num_blocks: int, num_layers: int, num_kv_heads: int,
                 block_size: int, head_dim: int):
        self.num_blocks = int(num_blocks)
        self.num_layers = int(num_layers)
        self.num_kv_heads = int(num_kv_heads)
        self.block_size = int(block_size)
        self.head_dim = int(head_dim)

        self._k_pools: List[Optional[torch.Tensor]] = [None] * self.num_layers
        self._v_pools: List[Optional[torch.Tensor]] = [None] * self.num_layers
        self._refcount = [0] * self.num_blocks
        self._free: "OrderedDict[int, None]" = OrderedDict(
            (i, None) for i in range(self.num_blocks)
        )
        self._on_evict = None
        self.lock = threading.RLock()

    # ------------------------------------------------------------------
    # Refcount / allocation
    # ------------------------------------------------------------------

    def set_on_evict(self, fn) -> None:
        self._on_evict = fn

    def refcount(self, bid: int) -> int:
        with self.lock:
            return self._refcount[bid]

    def free_count(self) -> int:
        with self.lock:
            return len(self._free)

    def used_count(self) -> int:
        with self.lock:
            return sum(1 for r in self._refcount if r > 0)

    def _alloc_locked(self) -> int:
        if not self._free:
            raise OutOfBlocks(
                f"hết block: {self.num_blocks} block đều đang được dùng"
            )
        bid, _ = self._free.popitem(last=False)
        self._refcount[bid] = 1
        if self._on_evict is not None:
            self._on_evict(bid)
        return bid

    def alloc(self) -> int:
        with self.lock:
            return self._alloc_locked()

    def _incref_locked(self, bid: int) -> None:
        if bid in self._free:
            del self._free[bid]
        self._refcount[bid] += 1

    def incref(self, bid: int) -> None:
        with self.lock:
            self._incref_locked(bid)

    def _decref_locked(self, bid: int) -> None:
        self._refcount[bid] -= 1
        if self._refcount[bid] <= 0:
            self._refcount[bid] = 0
            self._free[bid] = None
            self._free.move_to_end(bid)

    def decref(self, bid: int) -> None:
        with self.lock:
            self._decref_locked(bid)

    # ------------------------------------------------------------------
    # Storage
    # ------------------------------------------------------------------

    def _pool_locked(self, pools, layer: int, ref: torch.Tensor) -> torch.Tensor:
        pool = pools[layer]
        if pool is None:
            pool = torch.zeros(
                (self.num_blocks, self.num_kv_heads, self.block_size, self.head_dim),
                dtype=ref.dtype,
                device=ref.device,
            )
            pools[layer] = pool
        return pool

    def write(self, bid: int, layer: int, start: int,
              k: torch.Tensor, v: torch.Tensor) -> None:
        """Ghi `k`/`v` shape `[H, n, D]` vào block `bid` tại offset `start`."""
        with self.lock:
            kp = self._pool_locked(self._k_pools, layer, k)
            vp = self._pool_locked(self._v_pools, layer, v)
            n = int(k.shape[1])
            kp[bid, :, start : start + n, :] = k
            vp[bid, :, start : start + n, :] = v

    def gather(self, block_table: List[int], length: int, layer: int):
        """Nối K/V theo `block_table`, cắt theo `length` → `[1, H, length, D]`."""
        with self.lock:
            kp = self._k_pools[layer]
            vp = self._v_pools[layer]
            if kp is None:
                raise RuntimeError(f"layer {layer} chưa có pool (chưa ghi gì)")
            out_k = kp.new_zeros((1, self.num_kv_heads, int(length), self.head_dim))
            out_v = vp.new_zeros((1, self.num_kv_heads, int(length), self.head_dim))
            for bi, bid in enumerate(block_table):
                start = bi * self.block_size
                if start >= length:
                    break
                take = min(self.block_size, length - start)
                out_k[:, :, start : start + take, :] = kp[bid, :, :take, :]
                out_v[:, :, start : start + take, :] = vp[bid, :, :take, :]
            return out_k, out_v

    def copy_on_write(self, bid: int) -> int:
        """Block `bid` đang chia sẻ (refcount>1): copy sang block mới, trả id mới."""
        with self.lock:
            new_bid = self._alloc_locked()
            for layer in range(self.num_layers):
                self._k_pools[layer][new_bid] = self._k_pools[layer][bid].clone()
                self._v_pools[layer][new_bid] = self._v_pools[layer][bid].clone()
            self._decref_locked(bid)
            return new_bid


class PagedKVCache:
    """KV cache của một sequence dưới dạng block table."""

    def __init__(self, manager: BlockManager):
        self.m = manager
        self.block_table: List[int] = []
        self.length = 0

    @property
    def capacity(self) -> int:
        return len(self.block_table) * self.m.block_size

    # ------------------------------------------------------------------
    # Ghi
    # ------------------------------------------------------------------

    def _write_at(self, pos: int, layer: int, k: torch.Tensor, v: torch.Tensor) -> None:
        bi = pos // self.m.block_size
        off = pos % self.m.block_size
        if bi == len(self.block_table):
            self.block_table.append(self.m.alloc())
        bid = self.block_table[bi]
        if self.m.refcount(bid) > 1:
            bid = self.m.copy_on_write(bid)
            self.block_table[bi] = bid
        self.m.write(bid, layer, off, k, v)

    def init_from_prefill(self, past, prompt_len: int, row: int = 0) -> None:
        """Ghi các vị trí `[length, prompt_len)` từ `past[row]` (legacy tuple).

        Nếu sequence đã adopt prefix (length>0, block-aligned) thì chỉ ghi **suffix**
        — prefix giữ nguyên block chia sẻ (không copy).
        """
        start = self.length
        S = int(past[0][0].shape[2])
        for p in range(start, prompt_len):
            src = S - prompt_len + p
            for layer, (k, v) in enumerate(past):
                self._write_at(
                    p, layer,
                    k[row, :, src, :].unsqueeze(1),
                    v[row, :, src, :].unsqueeze(1),
                )
            self.length += 1

    def append_from_output(self, past, row: int) -> None:
        """Ghi slot cuối của `past[row]` (token vừa sinh) vào block kế tiếp."""
        pos = self.length
        for layer, (k, v) in enumerate(past):
            self._write_at(
                pos, layer,
                k[row, :, -1:, :],
                v[row, :, -1:, :],
            )
        self.length += 1

    # ------------------------------------------------------------------
    # Chia sẻ block
    # ------------------------------------------------------------------

    def adopt(self, block_ids: List[int], incref: bool = True) -> None:
        """Nhận block đã có làm prefix — không copy dữ liệu.

        `incref=False` khi ref đã được giữ bởi `BlockPrefixCache.match` (engine
        truyền prefix theo cách này để `free()` nhả đúng một lần).
        """
        for bid in block_ids:
            if incref:
                self.m.incref(bid)
            self.block_table.append(bid)
        self.length = len(block_ids) * self.m.block_size

    def fork(self) -> "PagedKVCache":
        """Tạo cache con chia sẻ toàn bộ block (CoW khi ghi)."""
        child = PagedKVCache(self.m)
        for bid in self.block_table:
            self.m.incref(bid)
            child.block_table.append(bid)
        child.length = self.length
        return child

    def view(self):
        """Gather theo block table → list `(k, v)` mỗi layer `[1, H, length, D]`."""
        return [
            self.m.gather(self.block_table, self.length, layer)
            for layer in range(self.m.num_layers)
        ]

    def free(self) -> None:
        for bid in self.block_table:
            self.m.decref(bid)
        self.block_table = []
        self.length = 0


class BlockPrefixCache:
    """Prefix cache dựa trên block id (chia sẻ thật, refcount).

    `match()` incref các block khớp để sequence adopt; `insert()` đăng ký hash cho
    các block đủ của sequence sau khi chạy xong. `BlockManager.on_evict` xoá mapping
    khi block bị tái dùng.
    """

    def __init__(self, manager: BlockManager, block_size: Optional[int] = None,
                 max_blocks: Optional[int] = None):
        self.m = manager
        self.block_size = int(block_size or manager.block_size)
        self.max_blocks = int(max_blocks) if max_blocks is not None else manager.num_blocks
        self._map: "OrderedDict[bytes, int]" = OrderedDict()
        self._rev: dict = {}
        prev = manager._on_evict

        def on_evict(bid: int) -> None:
            if prev is not None:
                prev(bid)
            self._drop_bid(bid)

        manager.set_on_evict(on_evict)

    def __len__(self) -> int:
        return len(self._map)

    def clear(self) -> None:
        with self.m.lock:
            self._map.clear()
            self._rev.clear()

    def _drop_bid(self, bid: int) -> None:
        child = self._rev.pop(bid, None)
        if child is not None:
            self._map.pop(child, None)

    def match(self, token_ids) -> Tuple[int, Optional[List[int]]]:
        """Longest-prefix match (block-aligned), chừa ≥1 token để prefill."""
        bs = self.block_size
        n = len(token_ids)
        max_full = ((n - 1) // bs) * bs
        if max_full <= 0:
            return 0, None
        parent = b""
        ids: List[int] = []
        with self.m.lock:
            b = 0
            while (b + 1) * bs <= max_full:
                toks = tuple(token_ids[b * bs : (b + 1) * bs])
                child = _block_hash(parent, toks)
                bid = self._map.get(child)
                if bid is None:
                    break
                self._map.move_to_end(child)
                self.m._incref_locked(bid)
                ids.append(bid)
                parent = child
                b += 1
        if not ids:
            return 0, None
        return b * bs, ids

    def insert(self, token_ids, cache: "PagedKVCache") -> None:
        """Đăng ký hash cho các block **đủ** của `cache` (chưa có mapping)."""
        table = getattr(cache, "block_table", None)
        length = int(getattr(cache, "length", 0))
        if not table:
            return
        bs = self.block_size
        n = min(length, len(token_ids), len(table) * bs)
        with self.m.lock:
            parent = b""
            for b in range(n // bs):
                toks = tuple(token_ids[b * bs : (b + 1) * bs])
                child = _block_hash(parent, toks)
                if child in self._map:
                    self._map.move_to_end(child)
                else:
                    bid = table[b]
                    # Giữ `_map`/`_rev` là song ánh: không ghi đè rev của bid.
                    if bid not in self._rev:
                        self._map[child] = bid
                        self._rev[bid] = child
                parent = child
            while len(self._map) > self.max_blocks:
                old_child, old_bid = self._map.popitem(last=False)
                if self._rev.get(old_bid) == old_child:
                    del self._rev[old_bid]
