# Stops the NSSM-managed Python inference worker and stops it from auto-starting
# with Windows (StartupType = Manual). Run in an ELEVATED PowerShell.
#
#   powershell -ExecutionPolicy Bypass -File scripts\disable-python-worker-service.ps1
#
# Re-enable auto-start later with:
#   Set-Service -Name AIFactoryWorker -StartupType Automatic

$ErrorActionPreference = "Stop"

$identity  = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Error "Please run this in an elevated PowerShell (Run as Administrator)."
    exit 1
}

$service = "AIFactoryWorker"
$svc = Get-Service -Name $service -ErrorAction SilentlyContinue
if (-not $svc) {
    Write-Host "Service '$service' not found. Nothing to do."
    exit 0
}

if ($svc.Status -ne "Stopped") {
    Write-Host "Stopping '$service'..."
    Stop-Service -Name $service -Force
}

Set-Service -Name $service -StartupType Manual
Write-Host "Done: '$service' is stopped and set to Manual (no auto-start at boot)."
Write-Host "Start it manually when needed with: Start-Service $service"
