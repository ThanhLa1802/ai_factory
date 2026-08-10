# Tải GGUF Qwen3.5-9B Q4_K_M vào models/
# Usage: .\download_qwen35.ps1 [-Repo unsloth/Qwen3.5-9B-GGUF] [-File Qwen3.5-9B-Q4_K_M.gguf]
# Lưu ý repo/file: Qwen/Qwen3.5-9B-GGUF (chính thức) bị gated (HTTP 401).
#   Primary: unsloth/Qwen3.5-9B-GGUF — Qwen3.5-9B-Q4_K_M.gguf (~5.68 GB)
#   Fallback: lmstudio-community/Qwen3.5-9B-GGUF (cùng file Q4_K_M) nếu unsloth thay đổi/gated.
param(
    [string]$Repo = "unsloth/Qwen3.5-9B-GGUF",
    [string]$File = "Qwen3.5-9B-Q4_K_M.gguf"
)
$ErrorActionPreference = "Stop"
$root = Split-Path $PSScriptRoot -Parent
$dest = Join-Path $root "models"
New-Item -ItemType Directory -Force -Path $dest | Out-Null
Write-Host "Downloading $Repo/$File -> $dest"
python -c "from huggingface_hub import hf_hub_download; print(hf_hub_download('$Repo','$File',local_dir=r'$dest'))"
