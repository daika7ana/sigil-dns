# installer/install.ps1 - elevated Windows installer for dns-client.
#
# Copies dns-client.exe into Program Files, creates the data directory, and
# registers + starts the service using the product's own `service` commands so
# behaviour matches the app.  Idempotent / upgrade-safe: an existing service is
# stopped and removed before the binary is replaced.
#
# Installs both the console binary (CLI + service + --apply helper) and the GUI
# binary, and drops a Start Menu shortcut to the GUI status window.
#
# Usage (from an elevated PowerShell):
#   .\install.ps1
#   .\install.ps1 -Source C:\path\to\dns-client.exe
#   .\install.ps1 -DesktopShortcut

[CmdletBinding()]
param(
    # Path to the dns-client.exe to install.  Defaults to the copy next to the
    # installer's parent directory (as laid out in the release zip).
    [string] $Source,

    # Path to the dns-client-gui.exe to install.  Defaults to the sibling copy
    # in the release zip.  Optional: a missing GUI binary warns and installs the
    # service + CLI only.
    [string] $GuiSource,

    # Also create a Desktop shortcut (a Start Menu shortcut is always created).
    [switch] $DesktopShortcut,

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

function New-GuiShortcut {
    param(
        [Parameter(Mandatory)] [string] $Path,
        [Parameter(Mandatory)] [string] $Target
    )

    $shell = New-Object -ComObject WScript.Shell
    $lnk = $shell.CreateShortcut($Path)
    $lnk.TargetPath = $Target
    $lnk.WorkingDirectory = $InstallDir
    $lnk.IconLocation = "$Target,0"
    $lnk.Description = 'dns-client status window'
    $lnk.Save()
}

Assert-Elevated

if (-not $Source) {
    $Source = Join-Path (Split-Path -Parent $PSScriptRoot) 'dns-client.exe'
}
if (-not (Test-Path -LiteralPath $Source)) {
    throw "dns-client.exe not found at '$Source'. Pass -Source <path-to-exe>."
}

if (-not $GuiSource) {
    $GuiSource = Join-Path (Split-Path -Parent $PSScriptRoot) 'dns-client-gui.exe'
}

$targetExe = Join-Path $InstallDir 'dns-client.exe'
$guiExe = Join-Path $InstallDir 'dns-client-gui.exe'
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

$guiInstalled = Test-Path -LiteralPath $GuiSource
if ($guiInstalled) {
    Write-Host "Installing $GuiSource -> $guiExe"
    Copy-Item -LiteralPath $GuiSource -Destination $guiExe -Force
}
else {
    Write-Warning "dns-client-gui.exe not found at '$GuiSource'; installing the service + CLI only."
}

# Seed a default config if none exists (config init ACLs the file + dir).  An
# existing config is left untouched.
$configPath = Join-Path $DataDir 'config.yaml'
if (-not (Test-Path -LiteralPath $configPath)) {
    Invoke-Cli -Exe $targetExe -CliArgs @('config', 'init')
}

# Belt-and-suspenders: the data dir holds config + state, which may contain
# resolver ids.  Restrict it to SYSTEM + Administrators (inheritable).  Use the
# locale-independent SID form (*S-1-5-18 = SYSTEM, *S-1-5-32-544 = Administrators)
# so the grant does not depend on the OS display language.
icacls $DataDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' /grant:r '*S-1-5-32-544:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Failed to set ACLs on $DataDir (exit $LASTEXITCODE)."
}

Invoke-Cli -Exe $targetExe -CliArgs @('service', 'install')
Invoke-Cli -Exe $targetExe -CliArgs @('service', 'start')

if ($guiInstalled) {
    $shortcut = Join-Path (Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs') 'dns-client.lnk'
    New-GuiShortcut -Path $shortcut -Target $guiExe
    Write-Host "Created Start Menu shortcut: $shortcut"

    if ($DesktopShortcut) {
        $desktop = Join-Path (Join-Path $env:PUBLIC 'Desktop') 'dns-client.lnk'
        New-GuiShortcut -Path $desktop -Target $guiExe
        Write-Host "Created Desktop shortcut: $desktop"
    }
}

Write-Host ''
Write-Host "dns-client installed."
Write-Host "  Binary: $targetExe"
if ($guiInstalled) {
    Write-Host "  GUI:    $guiExe (Start Menu > dns-client)"
}
Write-Host "  Data:   $DataDir"
Write-Host ''
Write-Host 'Next steps:'
Write-Host "  1. Edit $configPath (set mode: on, pick a provider)"
Write-Host "  2. $targetExe service stop; $targetExe service start   # apply"
Write-Host ''
Write-Host 'See config.example.yaml in the release zip for sample profiles.'
