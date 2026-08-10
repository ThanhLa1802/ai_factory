"""BPETokenizer — byte-level BPE tự triển khai (thay thế HF tokenizer trong pipeline).

Load vocab + merges có sẵn của Qwen 2.5 (không retrain) và tự viết thuật toán:
byte-encoder, regex pre-tokenization, BPE merge, decode, added tokens, batch,
và decode tăng dần (streaming).

Config lấy từ tokenizer.json / tokenizer_config.json của model (xem spec
docs/superpowers/specs/2026-08-08-tokenizer-design.md). Hai hằng số dưới đây
được hardcode theo đúng file config của Qwen 2.5.
"""

import codecs
import json
import unicodedata

# Dùng module `regex` (dependency của tokenizers/transformers) thay vì `re`
# vì pattern pre-tokenization dùng \p{L} / \p{N} — Python `re` không hỗ trợ.
import regex as re

from transformers.utils import cached_file

from .byte_level import bytes_to_unicode

# Regex pre-tokenization của Qwen 2.5 (lấy nguyên từ tokenizer.json, nhánh
# Split behavior=Isolated). Tách text thành cụm trước khi BPE:
#   - từ viết tắt ('s, 't, 're, … — phân biệt hoa thường)
#   - chuỗi chữ cái \p{L}, chuỗi số \p{N}, ký tự đặc biệt, dòng mới, khoảng trắng
_PRE_TOKENIZE_RE = re.compile(
    r"(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+"
)


def _get_pairs(word):
    """Trả về set các cặp ký tự liên tiếp (cho thuật toán merge)."""
    pairs = set()
    prev = word[0]
    for ch in word[1:]:
        pairs.add((prev, ch))
        prev = ch
    return pairs


def _tok_value(x):
    """tokenizer_config đôi khi lưu token dạng str, đôi khi dạng dict {"content": ...}."""
    if isinstance(x, dict):
        return x.get("content")
    return x


class BPETokenizer:
    """Byte-level BPE tokenizer tương thích Qwen 2.5."""

    def __init__(self, vocab_file: str, merges_file: str, config_file: str):
        with open(vocab_file, encoding="utf-8") as f:
            self.encoder = json.load(f)          # token -> id
        self.decoder = {v: k for k, v in self.encoder.items()}  # id -> token

        # byte ↔ unicode
        self.byte_encoder = bytes_to_unicode()   # byte (0-255) -> unicode char
        self.byte_decoder = {v: k for k, v in self.byte_encoder.items()}

        # merges: danh sách cặp token theo thứ tự train → rank
        with open(merges_file, encoding="utf-8") as f:
            merges = [line.rstrip("\n").split() for line in f]
        self.ranks = {tuple(pair): i for i, pair in enumerate(m for m in merges if len(m) == 2)}

        # ── Added tokens (từ tokenizer_config.json) ──
        # Qwen 2.5 có 22 added tokens, chia 2 nhóm:
        #   - special=True  (14, id 151643-151656): bị skip khi decode(skip_special_tokens=True)
        #   - special=False (8,  id 151657-151664): KHÔNG bị skip — decode ra literal
        #                 (VD <tool_call>, </tool_call>, <|fim_*|>, <|repo_name|>, <|file_sep|>)
        # Cả hai nhóm đều được tách + map sang id khi encode.
        with open(config_file, encoding="utf-8") as f:
            cfg = json.load(f)
        added = cfg.get("added_tokens_decoder", {})
        self.added_tokens_decoder = {int(k): v["content"] for k, v in added.items()}
        self.added_tokens_encoder = {v: k for k, v in self.added_tokens_decoder.items()}
        self.all_added_tokens = sorted(self.added_tokens_encoder, key=len, reverse=True)
        self.added_tokens_re = re.compile(
            "(" + "|".join(re.escape(t) for t in self.all_added_tokens) + ")"
        )
        self.all_special_ids = {int(k) for k, v in added.items() if v.get("special")}

        # eos / pad — đúng id của Qwen 2.5 (có fallback nếu config thiếu)
        eos = _tok_value(cfg.get("eos_token"))
        pad = _tok_value(cfg.get("pad_token"))
        self.eos_token_id = self.added_tokens_encoder.get(eos, 151645)  # <|im_end|>
        self.pad_token_id = self.added_tokens_encoder.get(pad, 151643)  # <|endoftext|>

        # cache kết quả BPE merge (nhiều từ lặp lại trong corpus)
        self._bpe_cache = {}

    # ------------------------------------------------------------------
    # Nạp từ HF cache
    # ------------------------------------------------------------------
    @classmethod
    def from_pretrained(cls, model_id: str) -> "BPETokenizer":
        vocab_file = cached_file(model_id, "vocab.json")
        merges_file = cached_file(model_id, "merges.txt")
        config_file = cached_file(model_id, "tokenizer_config.json")
        return cls(vocab_file, merges_file, config_file)

    # ------------------------------------------------------------------
    # Encode
    # ------------------------------------------------------------------
    @property
    def vocab_size(self) -> int:
        return len(self.encoder)

    @property
    def all_special_tokens(self) -> list:
        """Danh sách added token có special=True (được skip khi decode)."""
        return [self.added_tokens_decoder[tid] for tid in sorted(self.all_special_ids)]

    def encode(self, text: str, special: bool = True) -> list:
        """Token hoá text → list id.

        - `special=True` (mặc định): added token literal (VD `<|im_start|>`,
          `<tool_call>`) được tách ra và map thẳng sang id của chúng.
        - Không thêm BOS/EOS giả — chỉ token hoá nội dung có trong text
          (tương đương HF `add_special_tokens=False`, hành vi của Qwen).
        """
        if not isinstance(text, str):
            raise TypeError(f"encode() expects str, got {type(text)}")
        text = unicodedata.normalize("NFC", text)

        ids = []
        if special:
            for piece in self.added_tokens_re.split(text):
                if not piece:
                    continue
                if piece in self.added_tokens_encoder:
                    ids.append(self.added_tokens_encoder[piece])
                else:
                    ids.extend(self._encode_piece(piece))
        else:
            ids.extend(self._encode_piece(text))
        return ids

    def _encode_piece(self, piece: str) -> list:
        ids = []
        for word in _PRE_TOKENIZE_RE.findall(piece):
            for token in self._bpe(word.encode("utf-8")):
                ids.append(self.encoder[token])
        return ids

    def _bpe(self, token_bytes: bytes) -> tuple:
        """Byte-level BPE merge một cụm (bytes) → tuple các token (str).

        Mỗi byte → 1 unicode char (byte_encoder); merge các cặp theo rank từ
        merges.txt. Vì mọi byte-char đơn đều có trong vocab, base case luôn hợp lệ.
        """
        token = tuple(self.byte_encoder[b] for b in token_bytes)
        if token in self._bpe_cache:
            return self._bpe_cache[token]

        word = list(token)
        pairs = _get_pairs(word)

        while pairs:
            bigram = min(pairs, key=lambda pair: self.ranks.get(pair, float("inf")))
            if bigram not in self.ranks:
                break
            first, second = bigram
            new_word = []
            i = 0
            while i < len(word):
                try:
                    j = word.index(first, i)
                except ValueError:
                    new_word.extend(word[i:])
                    break
                new_word.extend(word[i:j])
                i = j
                if i < len(word) - 1 and word[i] == first and word[i + 1] == second:
                    new_word.append(first + second)
                    i += 2
                else:
                    new_word.append(word[i])
                    i += 1
            word = new_word
            if len(word) == 1:
                break
            pairs = _get_pairs(word)

        result = tuple(word)
        self._bpe_cache[token] = result
        return result

    # ------------------------------------------------------------------
    # Decode
    # ------------------------------------------------------------------
    def decode(self, token_ids, skip_special_tokens: bool = False, **kwargs) -> str:
        """Giải mã list id (hoặc một id) → text.

        - `skip_special_tokens=True`: bỏ các added token có `special=True`
          (VD `<|im_end|>`); các added token không special như `<tool_call>`
          vẫn decode ra literal — đúng hành vi HF.
        - Token thường → unescape byte → utf-8, `errors="replace"` cho đuôi
          dở (quan trọng khi stream từng token).
        """
        if isinstance(token_ids, int):
            token_ids = [token_ids]
        out = bytearray()
        for tid in token_ids:
            if tid in self.all_special_ids:
                if not skip_special_tokens:
                    out += self.added_tokens_decoder[tid].encode("utf-8")
            elif tid in self.added_tokens_decoder:
                out += self.added_tokens_decoder[tid].encode("utf-8")
            else:
                token = self.decoder[tid]
                for ch in token:
                    out.append(self.byte_decoder[ch])
        return out.decode("utf-8", errors="replace")

    # ------------------------------------------------------------------
    # Batch & inputs cho model
    # ------------------------------------------------------------------
    def encode_batch(self, texts, max_length: int = None, truncate: bool = False) -> list:
        ids_list = [self.encode(t) for t in texts]
        if truncate and max_length:
            ids_list = [ids[:max_length] for ids in ids_list]
        return ids_list

    def build_inputs(self, texts, max_length: int = None, truncate: bool = True,
                     add_special_tokens: bool = False) -> dict:
        """Tương đương `tokenizer(texts, return_tensors="pt", padding=True, truncation=...)`.

        - `add_special_tokens=False` (mặc định): Qwen fast tokenizer KHÔNG thêm
          BOS/EOS giả khi gọi `__call__` (đã xác minh), nên ta cũng không thêm
          để giữ hành vi pipeline hiện tại.
        - Trả về dict `{"input_ids": Tensor, "attention_mask": Tensor}`.
        """
        import torch

        ids_list = self.encode_batch(texts, max_length=max_length, truncate=truncate)
        if add_special_tokens:
            ids_list = [ids + [self.eos_token_id] for ids in ids_list]

        if not ids_list:
            return {
                "input_ids": torch.zeros(0, 0, dtype=torch.long),
                "attention_mask": torch.zeros(0, 0, dtype=torch.long),
            }

        max_len = max(len(ids) for ids in ids_list)
        padded, attn = [], []
        for ids in ids_list:
            pad_len = max_len - len(ids)
            padded.append(ids + [self.pad_token_id] * pad_len)
            attn.append([1] * len(ids) + [0] * pad_len)
        return {
            "input_ids": torch.tensor(padded, dtype=torch.long),
            "attention_mask": torch.tensor(attn, dtype=torch.long),
        }

    def token_count(self, text: str) -> int:
        return len(self.encode(text))


class StreamingDecoder:
    """Decode token dần dần, buffer byte đang dở giữa các token.

    Dùng incremental utf-8 decoder: nếu token cuối cắt giữa một ký tự đa byte,
    phần đuôi được giữ lại đến khi token tiếp theo (hoặc flush) hoàn thiện nó.
    """

    def __init__(self, tokenizer: BPETokenizer, skip_special_tokens: bool = True):
        self.tokenizer = tokenizer
        self.skip_special_tokens = skip_special_tokens
        self._dec = codecs.getincrementaldecoder("utf-8")(errors="replace")

    def put(self, token_id: int) -> str:
        """Nhận một token id, trả về text hoàn chỉnh mới (có thể rỗng)."""
        tok = self.tokenizer
        chunk = bytearray()
        if token_id in tok.added_tokens_decoder:
            # skip chỉ các added token special (khớp hành vi HF decode)
            if not (token_id in tok.all_special_ids and self.skip_special_tokens):
                chunk += tok.added_tokens_decoder[token_id].encode("utf-8")
        else:
            token = tok.decoder[token_id]
            for ch in token:
                chunk.append(tok.byte_decoder[ch])
        if not chunk:
            return ""
        return self._dec.decode(bytes(chunk))

    def flush(self) -> str:
        """Xả phần còn lại khi stream kết thúc."""
        return self._dec.decode(b"", final=True)
