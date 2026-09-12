# Runs the Python inference worker in the foreground, without NSSM or any
# auto-start. Ctrl+C to stop. Override the paths via parameters if needed.
#
#   powershell -ExecutionPolicy Bypass -File scripts\run-python-worker.ps1

[CmdletBinding()]
param(
    [string]$Python   = "C:\Users\thanh\anaconda3\envs\mywork\python.exe",
    [string]$Engine   = "llama",
    [string]$GGUF     = "G:\models\Qwen3.5-9B-Q4_K_M.gguf",
    [string]$LlamaBin = "G:\models\llama.cpp\llama-server.exe"
)

$ErrorActionPreference = "Stop"
$workerDir = (Resolve-Path (Join-Path $PSScriptRoot "..\python-worker")).Path
Set-Location $workerDir

Write-Host "Starting Python worker (engine=$Engine) in $workerDir"
Write-Host "Press Ctrl+C to stop."

& $Python -u -m worker.server --engine $Engine --gguf $GGUF --llama-bin $LlamaBin
