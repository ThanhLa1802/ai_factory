#!/usr/bin/env bash
# Tải GGUF Qwen3.5-9B Q4_K_M vào models/
# Usage: ./download_qwen35.sh [REPO] [FILE]
# Lưu ý repo/file: Qwen/Qwen3.5-9B-GGUF (chính thức) bị gated (HTTP 401).
#   Primary: unsloth/Qwen3.5-9B-GGUF — Qwen3.5-9B-Q4_K_M.gguf (~5.68 GB)
#   Fallback: lmstudio-community/Qwen3.5-9B-GGUF (cùng file Q4_K_M) nếu unsloth thay đổi/gated.
set -euo pipefail
REPO="${1:-unsloth/Qwen3.5-9B-GGUF}"
FILE="${2:-Qwen3.5-9B-Q4_K_M.gguf}"
DEST="$(dirname "$(dirname "$0")")/models"
mkdir -p "$DEST"
python -c "from huggingface_hub import hf_hub_download; print(hf_hub_download('$REPO','$FILE',local_dir=r'$DEST'))"
