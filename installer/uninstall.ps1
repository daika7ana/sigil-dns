# installer/uninstall.ps1 - elevated Windows uninstaller for dns-client.
#
# Stops the service, restores interception (best-effort) so no NRPT rule points
# at a dead resolver, removes the service, and deletes the program files.  By
# default the data directory (config + state) is kept; pass -RemoveData to delete
# it too.
#
# Usage (from an elevated PowerShell):
#   .\uninstall.ps1
#   .\uninstall.ps1 -RemoveData

[CmdletBinding()]
param(
    [switch] $RemoveData,

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
        throw 'This uninstaller must be run elevated: open PowerShell as Administrator and retry.'
    }
}

Assert-Elevated

$targetExe = Join-Path $InstallDir 'dns-client.exe'
$existingService = Get-Service -Name $serviceName -ErrorAction SilentlyContinue

if (Test-Path -LiteralPath $targetExe) {
    # Stop first: the service's shutdown restores interception and releases the
    # single-instance lock.
    & $targetExe service stop 2>$null | Out-Null

    # Belt-and-suspenders: clear any dirty/leftover interception from a crashed
    # run, now that the lock is free.  Best-effort.
    & $targetExe restore 2>$null | Out-Null

    if ($null -ne $existingService) {
        & $targetExe service uninstall
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to remove the '$serviceName' service (exit $LASTEXITCODE)."
        }
    }
}
elseif ($null -ne $existingService) {
    throw "'$serviceName' is registered but '$targetExe' is missing; remove it with 'sc.exe delete $serviceName' and re-run."
}
else {
    Write-Host 'No installed dns-client service or binary found.'
}

if (Test-Path -LiteralPath $InstallDir) {
    Remove-Item -Recurse -Force -LiteralPath $InstallDir
    Write-Host "Removed $InstallDir"
}

if ($RemoveData) {
    if (Test-Path -LiteralPath $DataDir) {
        Remove-Item -Recurse -Force -LiteralPath $DataDir
        Write-Host "Removed data directory $DataDir"
    }
}
else {
    Write-Host "Kept data directory $DataDir (pass -RemoveData to delete config and state)."
}

Write-Host 'dns-client uninstalled.'
