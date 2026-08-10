#!/bin/bash
# AI Factory — Setup script
set -e

echo "=== AI Factory Setup ==="

# Python worker
echo ""
echo "--- Python Worker ---"
cd python-worker

# Create virtual environment
if [ ! -d ".venv" ]; then
    python -m venv .venv
    echo "Created .venv"
fi

# Activate and install
source .venv/Scripts/activate 2>/dev/null || source .venv/bin/activate
pip install -e ".[dev]"
# httpx + huggingface_hub đã nằm trong dependencies của pyproject.toml (cài qua ".[dev]").
# Tải GGUF: ./scripts/download_qwen35.sh  (repo unsloth/Qwen3.5-9B-GGUF, file Qwen3.5-9B-Q4_K_M.gguf)
# llama-server: tải bản *-bin-win-cuda-cu12.4-x64.zip từ https://github.com/ggml-org/llama.cpp/releases
#   → giải nén, thêm thư mục chứa llama-server.exe vào PATH (xem task-7-brief Step 3).

# Generate proto stubs
python -m worker.generate_proto
echo "Python worker ready."
cd ..

# Go server
echo ""
echo "--- Go Server ---"
cd go-server
go mod tidy
go build ./cmd/server/
echo "Go server ready."
cd ..

echo ""
echo "=== Setup complete ==="
echo ""
echo "To run:"
echo "  Terminal 1: cd python-worker && source .venv/bin/activate && python -m worker.server"
echo "  Terminal 2: cd go-server && go run ./cmd/server/"
echo ""
echo "Test: curl http://localhost:8080/health"
