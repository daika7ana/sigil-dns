# installer/install.ps1 - elevated Windows installer for dns-client.
#
# Copies dns-client.exe into Program Files, creates the data directory, and
# registers + starts the service using the product's own `service` commands so
# behaviour matches the app.  Idempotent / upgrade-safe: an existing service is
# stopped and removed before the binary is replaced.
#
# Usage (from an elevated PowerShell):
#   .\install.ps1
#   .\install.ps1 -Source C:\path\to\dns-client.exe
#
# The tray UI is a v1.1 feature; this ships the service + CLI only.

[CmdletBinding()]
param(
    # Path to the dns-client.exe to install.  Defaults to the copy next to the
    # installer's parent directory (as laid out in the release zip).
    [string] $Source,

    [string] $InstallDir = (Join-Path $env:ProgramFiles 'dns-client'),
    [string] $DataDir = (Join-Path $env:ProgramData 'dns-client')
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$serviceName = 'dns-client'

function Assert-Elevated {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'This installer must be run elevated: open PowerShell as Administrator and retry.'
    }
}

function Invoke-Cli {
    param(
        [Parameter(Mandatory)] [string]   $Exe,
        [Parameter(Mandatory)] [string[]] $CliArgs
    )

    & $Exe @CliArgs
    if ($LASTEXITCODE -ne 0) {
        throw "dns-client $($CliArgs -join ' ') failed with exit code $LASTEXITCODE"
    }
}

Assert-Elevated

if (-not $Source) {
    $Source = Join-Path (Split-Path -Parent $PSScriptRoot) 'dns-client.exe'
}
if (-not (Test-Path -LiteralPath $Source)) {
    throw "dns-client.exe not found at '$Source'. Pass -Source <path-to-exe>."
}

$targetExe = Join-Path $InstallDir 'dns-client.exe'
$existingService = Get-Service -Name $serviceName -ErrorAction SilentlyContinue

# Upgrade path: stop and remove any existing service (stop restores NRPT via the
# service's own shutdown), then replace the binary.
if ($null -ne $existingService) {
    Write-Host "Existing '$serviceName' service found; removing before upgrade..."

    if (Test-Path -LiteralPath $targetExe) {
        # Best-effort stop; a stopped/absent service returns non-zero, which is
        # fine because we remove it next.
        & $targetExe service stop 2>$null | Out-Null

        & $targetExe service uninstall
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to remove the existing '$serviceName' service (exit $LASTEXITCODE)."
        }
    }
    else {
        throw "'$serviceName' is registered but '$targetExe' is missing; remove it with 'sc.exe delete $serviceName' and re-run."
    }
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

Write-Host "Installing $Source -> $targetExe"
Copy-Item -LiteralPath $Source -Destination $targetExe -Force

Invoke-Cli -Exe $targetExe -CliArgs @('service', 'install')
Invoke-Cli -Exe $targetExe -CliArgs @('service', 'start')

Write-Host ''
Write-Host "dns-client installed."
Write-Host "  Binary: $targetExe"
Write-Host "  Data:   $DataDir"
Write-Host ''
Write-Host 'Next steps:'
Write-Host "  1. $targetExe config init          # writes $DataDir\config.yaml"
Write-Host '  2. Edit config.yaml (set mode: on, pick a provider)'
Write-Host "  3. $targetExe service stop; $targetExe service start   # apply"
Write-Host ''
Write-Host 'See config.example.yaml in the release zip for sample profiles.'
