"""Byte-level encoding — map mỗi byte (0-255) sang một unicode char duy nhất.

Đây là kỹ thuật byte-level BPE (GPT-2 / o200k / Qwen): mọi dãy byte đều có thể
đại diện bởi một dãy unicode char, và mỗi char đó có một token trong vocab.
Nhờ đó tokenizer xử lý được mọi ngôn ngữ / emoji / dữ liệu nhị phân mà không
bao giờ vỡ UTF-8 — vì "thế giới" của BPE là byte, không phải ký tự.

Map chuẩn GPT-2: các ký tự printable (0x21-0x7E, 0xA1-0xAC, 0xAE-0xFF) giữ
nguyên; các byte còn lại (space, control, NUL, …) được gán vào vùng U+0100+.
"""


def bytes_to_unicode() -> dict:
    """Trả về map byte (0-255) → unicode char, khớp với ByteLevel của HuggingFace."""
    bs = (
        list(range(ord("!"), ord("~") + 1))   # 0x21-0x7E
        + list(range(ord("¡"), ord("¬") + 1))  # 0xA1-0xAC
        + list(range(ord("®"), ord("ÿ") + 1))  # 0xAE-0xFF
    )
    cs = bs[:]
    n = 0
    for b in range(256):
        if b not in bs:
            bs.append(b)
            cs.append(256 + n)
            n += 1
    return dict(zip(bs, [chr(c) for c in cs]))
