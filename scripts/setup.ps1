# AI Factory — Setup script (PowerShell)
$ErrorActionPreference = "Stop"

Write-Host "=== AI Factory Setup ===" -ForegroundColor Cyan

# Python worker
Write-Host "`n--- Python Worker ---" -ForegroundColor Yellow
Set-Location python-worker

if (-not (Test-Path ".venv")) {
    python -m venv .venv
    Write-Host "Created .venv"
}

.venv\Scripts\Activate.ps1
pip install -e ".[dev]"

# Generate proto stubs
python -m worker.generate_proto
Write-Host "Python worker ready."

Set-Location ..

# Go server
Write-Host "`n--- Go Server ---" -ForegroundColor Yellow
Set-Location go-server
go mod tidy
go build ./cmd/server/
Write-Host "Go server ready."
Set-Location ..

Write-Host "`n=== Setup complete ===" -ForegroundColor Green
Write-Host ""
Write-Host "To run:" -ForegroundColor Cyan
Write-Host "  Terminal 1: cd python-worker; .venv\Scripts\Activate.ps1; python -m worker.server"
Write-Host "  Terminal 2: cd go-server; go run ./cmd/server/"
Write-Host ""
Write-Host "Test: curl http://localhost:8080/health"
