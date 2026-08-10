"""Tests cho own tokenizer (`worker.model.tokenizer`).

Cổng chính là **đối chiếu ID == HF**: tokenizer tự viết phải cho kết quả encode /
decode / padding giống hệt `AutoTokenizer` của Qwen 2.5 — vì nếu lệch dù 1 id thì
embedding của model sẽ không tương ứng, phá vỡ inference.

Các tầng kiểm tra:
- Round-trip: `decode(encode(s)) == s`
- Equivalence: `ours.encode(s, special=True) == hf(s, add_special_tokens=False)["input_ids"]`
- Special tokens: 22 added tokens của Qwen (kể cả `<tool_call>` — special=False)
- skip_special_tokens: chỉ skip nhóm special=True (khớp HF)
- Streaming: decode từng id + ghép == decode đầy đủ
- Batch: `build_inputs` pad/truncate/attention_mask khớp HF `padding=True`
- Duck-type: `decode` nhận cả int lẫn list[int] (cho TextIteratorStreamer)

Không cần GPU — chỉ dùng tokenizer, không load model.
"""

import pytest
from transformers import AutoTokenizer

from worker.model.tokenizer import BPETokenizer, StreamingDecoder

MODEL_ID = "Qwen/Qwen2.5-3B-Instruct"

# Corpus đại diện: tiếng Việt, English, code, emoji, tab/newline, số,
# special-token literal dùng trong thực tế (<tool_call>/</tool_call> — special=False).
CORPUS = [
    "Hello, world!",
    "Xin chào, tôi là một tokenizer.",
    "def hello(n):\n    return n + 1",
    "🎉🎊 emoji test 🚀",
    "\tindent\nnewline\ttab",
    "<tool_call>{\"name\":\"get_weather\",\"args\":{\"city\":\"HN\"}}</tool_call>",
    "This is <|im_start|>assistant<|im_end|> a test",
    "Numbers 123 456 7890.5",
    "Tôi thích lập trình 🐍 và đọc sách 📚.",
    "Mixed cám ơn thank you 谢谢 감사합니다 ありがとう",
    "<|repo_name|>my-repo<|file_sep|>main.py",
]


@pytest.fixture(scope="session")
def ours() -> BPETokenizer:
    return BPETokenizer.from_pretrained(MODEL_ID)


@pytest.fixture(scope="session")
def hf() -> AutoTokenizer:
    return AutoTokenizer.from_pretrained(MODEL_ID)


# ---------------------------------------------------------------------------
# Đặc tính cơ bản
# ---------------------------------------------------------------------------
class TestBasics:
    def test_vocab_size(self, ours):
        assert ours.vocab_size == 151643

    def test_eos_pad_ids(self, ours, hf):
        assert ours.eos_token_id == hf.eos_token_id == 151645  # <|im_end|>
        assert ours.pad_token_id == hf.pad_token_id == 151643  # <|endoftext|>

    def test_vocab_ids_in_range(self, ours):
        max_id = max(ours.encode("Hello world"))
        assert max_id < ours.vocab_size


# ---------------------------------------------------------------------------
# Encode: đối chiếu ID == HF
# ---------------------------------------------------------------------------
class TestEncodeEquivalence:
    @pytest.mark.parametrize("text", CORPUS)
    def test_encode_matches_hf(self, ours, hf, text):
        ours_ids = ours.encode(text, special=True)
        hf_ids = hf(text, add_special_tokens=False)["input_ids"]
        assert ours_ids == hf_ids

    def test_chat_template_prompt_matches_hf(self, ours, hf):
        """Prompt thực tế đi qua apply_chat_template (chứa <|im_start|>, <|im_end|>)."""
        messages = [
            {"role": "system", "content": "Bạn là trợ lý tiếng Việt."},
            {"role": "user", "content": "Giải thích cách hoạt động của BPE tokenizer?"},
        ]
        prompt = hf.apply_chat_template(messages, tokenize=False)
        assert ours.encode(prompt) == hf(prompt, add_special_tokens=False)["input_ids"]

    def test_no_synthetic_special_tokens(self, ours, hf):
        """Qwen fast tokenizer KHÔNG thêm BOS/EOS giả — mình cũng không thêm."""
        assert ours.encode("Hello") == hf("Hello")["input_ids"] == [9707]

    def test_encode_returns_list_of_int(self, ours):
        ids = ours.encode("abc")
        assert isinstance(ids, list)
        assert all(isinstance(i, int) for i in ids)


# ---------------------------------------------------------------------------
# Round-trip
# ---------------------------------------------------------------------------
class TestRoundtrip:
    @pytest.mark.parametrize("text", CORPUS)
    def test_decode_encodes_back(self, ours, text):
        assert ours.decode(ours.encode(text)) == text


# ---------------------------------------------------------------------------
# Special / added tokens
# ---------------------------------------------------------------------------
class TestSpecialTokens:
    def test_all_added_tokens_roundtrip(self, ours):
        """22 added tokens: encode(content) == [id] và decode(id) == content."""
        for content, tid in ours.added_tokens_encoder.items():
            assert ours.encode(content) == [tid]
            assert ours.decode([tid]) == content

    def test_special_true_ids_are_skipped(self, ours):
        for tid in sorted(ours.all_special_ids):
            assert ours.decode([tid], skip_special_tokens=True) == ""

    def test_non_special_added_not_skipped(self, ours):
        """special=False như <tool_call> vẫn decode ra literal — khớp HF."""
        tool_id = ours.added_tokens_encoder["<tool_call>"]
        assert tool_id not in ours.all_special_ids
        assert ours.decode([tool_id], skip_special_tokens=True) == "<tool_call>"

    def test_skip_matches_hf(self, ours, hf):
        for skip in (True, False):
            for text in ["<tool_call>x</tool_call>", "a <|im_start|>b<|im_end|>"]:
                ids = ours.encode(text)
                assert ours.decode(ids, skip_special_tokens=skip) == hf.decode(
                    ids, skip_special_tokens=skip
                )


# ---------------------------------------------------------------------------
# Duck-type decode (int | list[int]) cho TextIteratorStreamer
# ---------------------------------------------------------------------------
class TestDuckTypeDecode:
    def test_decode_accepts_int_and_list(self, ours):
        ids = ours.encode("Xin chào 123")
        assert ours.decode(ids[0]) == ours.decode([ids[0]])

    def test_decode_empty_list(self, ours):
        assert ours.decode([]) == ""


# ---------------------------------------------------------------------------
# Streaming decode
# ---------------------------------------------------------------------------
class TestStreaming:
    def test_streaming_matches_full_decode(self, ours):
        text = "Xin chào 🎉 mọi người! Đây là một câu có emoji và từ đa byte 谢谢."
        ids = ours.encode(text)
        sd = StreamingDecoder(ours, skip_special_tokens=True)
        parts = [sd.put(tid) for tid in ids]
        parts.append(sd.flush())
        assert "".join(parts) == ours.decode(ids, skip_special_tokens=True)

    def test_streaming_skips_special(self, ours):
        text = "hi<|im_end|>there"
        ids = ours.encode(text)
        sd = StreamingDecoder(ours, skip_special_tokens=True)
        assert "".join([sd.put(tid) for tid in ids] + [sd.flush()]) == "hithere"


# ---------------------------------------------------------------------------
# Batch: build_inputs
# ---------------------------------------------------------------------------
class TestBatch:
    def test_build_inputs_matches_hf(self, ours, hf):
        prompts = ["Hello world", "Xin chào", "Short", "a" * 200]
        ours_out = ours.build_inputs(prompts, max_length=100)
        hf_out = hf(
            prompts,
            add_special_tokens=False,
            padding=True,
            truncation=True,
            max_length=100,
            return_tensors="pt",
        )
        assert ours_out["input_ids"].tolist() == hf_out["input_ids"].tolist()
        assert ours_out["attention_mask"].tolist() == hf_out["attention_mask"].tolist()

    def test_build_inputs_truncates(self, ours):
        # "x"*500 bị BPE nén (nhiều x lặp → ít token), nên dùng 200 từ riêng biệt
        # để chắc chắn vượt max_length=100.
        long = " ".join(f"w{i}" for i in range(200))
        prompts = [long, "short"]
        out = ours.build_inputs(prompts, max_length=100)
        assert out["input_ids"].shape[1] == 100
        assert out["attention_mask"][1][0] == 1  # token thật của "short"

    def test_build_inputs_no_pad_ids_in_attn(self, ours):
        texts = ["hi", "hello world foo bar baz qux"]
        out = ours.build_inputs(texts)
        mask = out["attention_mask"].tolist()
        for i, t in enumerate(texts):
            assert mask[i].count(1) == len(ours.encode(t))
            assert mask[i][-1] == (1 if len(ours.encode(t)) == len(mask[i]) else 0)


# ---------------------------------------------------------------------------
# Đầu vào sai
# ---------------------------------------------------------------------------
class TestInputValidation:
    def test_encode_rejects_non_str(self, ours):
        with pytest.raises(TypeError):
            ours.encode(123)
