# Design — Tự triển khai tokenizer (byte-level BPE)

- **Ngày**: 2026-08-08
- **Trạng thái**: Đã duyệt (user: "duyệt, giữ HF cho template, làm luôn")
- **Phạm vi**: Roadmap Tuần 3-4 — tự viết thuật toán tokenizer, thay thế HF tokenizer trong pipeline, giữ tương thích 100% với Qwen 2.5 3B.

## 1. Mục tiêu & phạm vi

### Mục tiêu (học tập)
Tự viết **thuật toán byte-level BPE** thay cho HF `AutoTokenizer` trong cả hai đường inference (`engine.py` + `batch_engine.py`):
byte-encoder, regex pre-tokenization, BPE merge, decode, special tokens, batch encode/decode, streaming decode.

### Phạm vi
- **Load vocab có sẵn** (`vocab.json` + `merges.txt` + `tokenizer_config.json`) của Qwen từ HF cache → IDs khớp 100%, **không retrain, không làm vỡ model**.
- Chat template **giữ dùng HF** `apply_chat_template` (chỉ build prompt *string*; template ≠ tokenization) — quyết định D1 đã duyệt.
- Test **đối chiếu IDs == HF** trên corpus; round-trip; special tokens; streaming decode; batch pad/truncate.
- Không làm: sampling loop, KV cache, train BPE từ đầu, thay đổi proto.

### Ngoài phạm vi (loại trừ rõ ràng)
- Không train vocab mới (IDs sẽ không khớp embedding).
- Không tự viết Jinja chat template (nhánh `tools` phức tạp, rủi ro lệch prompt đã train).
- Không thay `model.generate()` — vẫn dùng HF generate + `TextIteratorStreamer`.

## 2. Kiến trúc module

```
python-worker/worker/model/tokenizer/
├── __init__.py      # export BPETokenizer, StreamingDecoder
├── byte_level.py    # byte_encoder / byte_decoder (map byte 0-255 → unicode, chuẩn GPT-2/Qwen)
└── bpe.py           # class BPETokenizer + class StreamingDecoder
```

### Cấu hình tokenizer Qwen 2.5 (đã xác minh từ tokenizer.json / tokenizer_config.json)
| Mục | Giá trị |
|---|---|
| Normalizer | NFC (`unicodedata.normalize`) |
| Pre-tokenizer | Sequence[ Split(regex, Isolated), ByteLevel ] |
| Regex | `(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+` |
| Model | BPE, byte_fallback=False, vocab 151,643, merges 151,387 |
| eos_token_id | **151645** (`<|im_end|>`) |
| pad_token_id | **151643** (`<|endoftext|>`) |
| Special tokens | 22 added tokens, ids 151643–151664. 14 nhóm `special=True` (151643–151656: `<|endoftext|>`, `<|im_start|>`, `<|im_end|>`, `<|object_ref_*|>`, `<|box_*|>`, `<|quad_*|>`, `<|vision_*|>`) — bị skip khi `decode(skip_special_tokens=True)`. 8 nhóm `special=False` (151657–151664: `<tool_call>`, `</tool_call>`, `<|fim_prefix|>`, `<|fim_middle|>`, `<|fim_suffix|>`, `<|fim_pad|>`, `<|repo_name|>`, `<|file_sep|>`) — KHÔNG bị skip, decode ra literal (đúng hành vi HF). |

## 3. API của BPETokenizer

### API chính (dùng trong pipeline)
- `encode(text, special=True) -> list[int]`
- `decode(ids, skip_special_tokens=False, **kwargs) -> str` — nhận list[int] **hoặc** int (duck-type cho TextIteratorStreamer); `errors="replace"` cho đuôi UTF-8 dở.
- `encode_batch(texts, max_length=None, truncate=False) -> list[list[int]]`
- `token_count(text) -> int` (= `len(encode(text))`)
- `build_inputs(texts, max_length=None) -> dict` — `{"input_ids": Tensor, "attention_mask": Tensor}` (thay cho `tokenizer(..., return_tensors="pt", padding=True)`).
- Thuộc tính: `eos_token_id`, `pad_token_id`, `vocab_size`, `all_special_tokens`.

### Streaming (decode tăng dần)
- `StreamingDecoder(tokenizer, skip_special_tokens=True)` với `put(token_id) -> str` (trả text hoàn chỉnh kể từ lần gọi trước) + `flush() -> str`.
- Dùng `codecs.getincrementaldecoder("utf-8")` để buffer byte dở đúng cách.

### Nạp từ HF cache
`BPETokenizer.from_pretrained(model_id)` dùng `transformers.utils.cached_file` để trỏ tới `vocab.json`, `merges.txt`, `tokenizer_config.json` (tải nếu thiếu).

## 4. Thuật toán

### Encode
```
text → NFC normalize → [nếu special] tách theo special tokens (regex "(" + "|".join(escape, longest-first) + ")") 
→ với mỗi phần: regex pre-tokenize → từng cụm → utf-8 bytes → byte_encoder (mỗi byte → 1 unicode char)
→ BPE merge (ranks từ merges.txt; mọi byte-char đơn đều có trong vocab) → token ids
```
- Special token (như `<|im_start|>`) → id trực tiếp, không qua BPE.
- `get_pairs` + vòng merge chuẩn GPT-2, cache kết quả (`self.cache`).

### Decode
```
id → nếu special: literal token string; ngược lại: token string → byte_decoder (mỗi char → 1 byte)
→ ghép byte stream → decode utf-8 với errors="replace"
```
- Special literal là ASCII thuần → trộn vào byte stream an toàn.
- `skip_special_tokens=True` → bỏ qua special (dùng cho streaming).

## 5. Tích hợp pipeline (thay đổi tối thiểu)

### `engine.py`
- `load()`: `self.tokenizer = BPETokenizer.from_pretrained(model_id)`; `self.hf_tokenizer = AutoTokenizer.from_pretrained(model_id)` — **chỉ** cho `apply_chat_template`.
- `_build_prompt()`: `self.tokenizer.apply_chat_template(...)` → `self.hf_tokenizer.apply_chat_template(...)`.
- `generate()`:
  - `self.tokenizer(prompt, return_tensors="pt").to(DEVICE)` → `self.tokenizer.build_inputs([prompt])` (dict tensors) → `.to(DEVICE)`.
  - `TextIteratorStreamer(self.tokenizer, ...)` — giữ nguyên; duck-type qua `decode(ids, **kwargs)` (đã xác minh: TextStreamer v4.50.3 chỉ gọi `tokenizer.decode`).
  - `pad_token_id`/`eos_token_id` từ BPETokenizer (151643 / 151645).
- `token_count()`: `len(self.tokenizer.encode(text))`.
- `unload()`: del cả hai.

### `batch_engine.py`
- `self.tokenizer(prompts, return_tensors="pt", padding=True, truncation=True, max_length=8192)` → `build_inputs(prompts, max_length=8192)`.
- `pad_token_id`/`eos_token_id`/`decode` từ BPETokenizer.

### `server.py`
- Không đổi. (`BatchEngine(engine.model, engine.tokenizer)` — `engine.tokenizer` giờ là BPETokenizer.)

## 6. Testing (`python-worker/tests/test_tokenizer.py`, pytest)

- **Round-trip**: `decode(encode(s, special=True)) == s` — corpus: tiếng Việt, English, code, emoji, tab/newline, special-token strings.
- **Đối chiếu == HF**: `ours.encode(text, special=True) == hf(text, add_special_tokens=False)["input_ids"]` — cùng corpus + chat-template prompt (chứa `<|im_start|>`, `<|im_end|>`).
- **Special tokens**: encode/decode id 151643–151645; cả 22 added tokens roundtrip; `<tool_call>` (special=False) không bị skip khi decode.
- **Streaming**: cắt mọi ranh giới byte — `StreamingDecoder` decode từng id, ghép == decode đầy đủ.
- **Batch**: `build_inputs` pad/truncate đúng attention_mask; so với HF `padding=True`.
- **Duck-type**: `decode` nhận cả int và list[int].
- E2E (thủ công, không tự động — cần GPU): chạy worker + Go server, 1 request.

## 7. Docs cập nhật

- `CONTEXT.md`: roadmap Tuần 3-4 → ✅; thêm glossary mục "Own Tokenizer".
- `docs/ARCHITECTURE.md`: §8 (model & inference) — tokenizer tự viết; §12 roadmap.

## 8. Rủi ro & xử lý

| Rủi ro | Xử lý |
|---|---|
| Regex/byte-encoder lệch HF → IDs sai | Test đối chiếu là cổng; pattern đã lấy chính xác từ tokenizer.json |
| Duck-type decode sai → streaming hỏng | TextStreamer v4.50.3 chỉ cần `decode`; test `decode` nhận int/list |
| Prompt tools (nhánh Jinja phức tạp) | Giữ `apply_chat_template` của HF — không đụng |
| Đuôi UTF-8 dở khi decode tăng dần | `errors="replace"` + print_len heuristic của streamer; `StreamingDecoder` dùng incremental codec |
