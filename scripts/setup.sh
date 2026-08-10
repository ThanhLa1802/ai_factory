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
