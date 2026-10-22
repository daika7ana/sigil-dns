# installer/setup.ps1 - interactive Windows installer / uninstaller for sigildns.
#
# One entry point: double-click, then pick Install or Uninstall from the menu.
# Started without elevation the script relaunches itself through UAC and shows
# the menu in the elevated window, so no elevated prompt has to be opened by
# hand.
#
# Non-interactive use (skips the menu):
#   .\setup.ps1 -Install [-DesktopShortcut] [-Source <sigildns.exe>] [-GuiSource <sigildns-gui.exe>]
#   .\setup.ps1 -Uninstall [-RemoveData]

[CmdletBinding()]
param(
    [switch] $Install,
    [switch] $Uninstall,

    # Uninstall: delete the data directory (config + state) as well.
    [switch] $RemoveData,

    # Install: also create a Desktop shortcut (a Start Menu shortcut is always
    # created).
    [switch] $DesktopShortcut,

    # Install: defaults to the sibling copy in the release zip.
    [string] $Source,
    [string] $GuiSource,

    [string] $InstallDir = (Join-Path $env:ProgramFiles 'SigilDNS'),
    [string] $DataDir = (Join-Path $env:ProgramData 'SigilDNS')
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Double-clicking the script (or "Run with PowerShell") starts a non-elevated
# shell.  Relaunch through UAC, forwarding the caller's parameters verbatim, so
# the menu appears in an elevated window.
$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    $relaunch = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-NoExit', '-File', "`"$PSCommandPath`"")
    foreach ($name in $PSBoundParameters.Keys) {
        $value = $PSBoundParameters[$name]
        if ($value -is [switch]) {
            if ($value.IsPresent) { $relaunch += "-$name" }
        }
        else {
            $relaunch += "-$name"
            $relaunch += "`"$value`""
        }
    }
    try {
        Start-Process powershell.exe -Verb RunAs -ArgumentList $relaunch
    }
    catch {
        Write-Host "Elevation was cancelled or refused: $($_.Exception.Message)" -ForegroundColor Red
        exit 1
    }
    exit 0
}

$serviceName = 'sigildns'

function Assert-Elevated {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'This script must be run elevated: open PowerShell as Administrator and retry.'
    }
}

function Invoke-Cli {
    param(
        [Parameter(Mandatory)] [string]   $Exe,
        [Parameter(Mandatory)] [string[]] $CliArgs
    )

    & $Exe @CliArgs
    if ($LASTEXITCODE -ne 0) {
        throw "Sigil DNS $($CliArgs -join ' ') failed with exit code $LASTEXITCODE"
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
    $lnk.Description = 'Sigil DNS status window'
    $lnk.Save()
}

function Install-SigilDns {
    # Default to the copy next to the installer directory (as laid out in the
    # release zip).
    $source = $Source
    if (-not $source) {
        $source = Join-Path (Split-Path -Parent $PSScriptRoot) 'sigildns.exe'
    }
    if (-not (Test-Path -LiteralPath $source)) {
        throw "sigildns.exe not found at '$source'. Pass -Source <path-to-exe>."
    }

    $guiSource = $GuiSource
    if (-not $guiSource) {
        $guiSource = Join-Path (Split-Path -Parent $PSScriptRoot) 'sigildns-gui.exe'
    }

    $targetExe = Join-Path $InstallDir 'sigildns.exe'
    $guiExe = Join-Path $InstallDir 'sigildns-gui.exe'
    $existingService = Get-Service -Name $serviceName -ErrorAction SilentlyContinue

    # Upgrade path: stop and remove any existing service (stop restores NRPT via
    # the service's own shutdown), then replace the binary.
    if ($null -ne $existingService) {
        Write-Host "Existing '$serviceName' service found; removing before upgrade..."

        if (Test-Path -LiteralPath $targetExe) {
            # Best-effort stop; a stopped/absent service returns non-zero, which
            # is fine because we remove it next.
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

    Write-Host "Installing $source -> $targetExe"
    Copy-Item -LiteralPath $source -Destination $targetExe -Force

    $guiInstalled = Test-Path -LiteralPath $guiSource
    if ($guiInstalled) {
        # A running GUI or windowless tray host locks the installed exe.  Wait
        # for the processes to release the image before copying over it.
        $guiProcs = @(Get-Process -Name 'sigildns-gui' -ErrorAction SilentlyContinue |
            Where-Object { $_.Path -eq $guiExe })
        $guiProcs | Stop-Process -Force -ErrorAction SilentlyContinue
        $guiProcs | Wait-Process -Timeout 5 -ErrorAction SilentlyContinue

        Write-Host "Installing $guiSource -> $guiExe"
        Copy-Item -LiteralPath $guiSource -Destination $guiExe -Force
    }
    else {
        Write-Warning "sigildns-gui.exe not found at '$guiSource'; installing the service + CLI only."
    }

    # Seed a default config if none exists (config init ACLs the file + dir).  An
    # existing config is left untouched.
    $configPath = Join-Path $DataDir 'config.yaml'
    if (-not (Test-Path -LiteralPath $configPath)) {
        Invoke-Cli -Exe $targetExe -CliArgs @('config', 'init')
    }

    # Belt-and-suspenders: the data dir holds config + state, which may contain
    # resolver ids.  Restrict it to SYSTEM + Administrators (inheritable).  Use
    # the locale-independent SID form (*S-1-5-18 = SYSTEM, *S-1-5-32-544 =
    # Administrators) so the grant does not depend on the OS display language.
    icacls $DataDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' /grant:r '*S-1-5-32-544:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to set ACLs on $DataDir (exit $LASTEXITCODE)."
    }

    Invoke-Cli -Exe $targetExe -CliArgs @('service', 'install')
    Invoke-Cli -Exe $targetExe -CliArgs @('service', 'start')

    if ($guiInstalled) {
        $shortcut = Join-Path (Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs') 'Sigil DNS.lnk'
        New-GuiShortcut -Path $shortcut -Target $guiExe
        Write-Host "Created Start Menu shortcut: $shortcut"

        if ($DesktopShortcut) {
            $desktop = Join-Path (Join-Path $env:PUBLIC 'Desktop') 'Sigil DNS.lnk'
            New-GuiShortcut -Path $desktop -Target $guiExe
            Write-Host "Created Desktop shortcut: $desktop"
        }
    }

    Write-Host ''
    Write-Host 'Sigil DNS installed.'
    Write-Host "  Binary: $targetExe"
    if ($guiInstalled) {
        Write-Host "  GUI:    $guiExe (Start Menu > Sigil DNS)"
    }
    Write-Host "  Data:   $DataDir"
    Write-Host ''
    Write-Host 'Next steps:'
    Write-Host "  1. Edit $configPath (pick a provider) or use the GUI"
    Write-Host "  2. $targetExe service stop; $targetExe service start   # apply"
    Write-Host ''
    Write-Host 'See config.example.yaml in the release zip for sample profiles.'
}

function Uninstall-SigilDns {
    $targetExe = Join-Path $InstallDir 'sigildns.exe'
    $existingService = Get-Service -Name $serviceName -ErrorAction SilentlyContinue

    if (Test-Path -LiteralPath $targetExe) {
        # Stop first: the service's shutdown restores interception and releases
        # the single-instance lock.
        & $targetExe service stop 2>$null | Out-Null
        if ($LASTEXITCODE -ne 0) {
            Write-Warning "'$serviceName' service stop returned exit $LASTEXITCODE; relying on the fail-closed 'service uninstall' below to clear interception."
        }

        # Belt-and-suspenders: clear any dirty/leftover interception from a
        # crashed run, now that the lock is free.  Best-effort; the fail-closed
        # uninstall below re-verifies interception is down before it deletes.
        & $targetExe restore 2>$null | Out-Null
        if ($LASTEXITCODE -ne 0) {
            Write-Warning "'$serviceName' restore returned exit $LASTEXITCODE; relying on the fail-closed 'service uninstall' below to clear interception."
        }

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
        Write-Host 'No installed Sigil DNS service or binary found.'
    }

    # Remove the GUI shortcuts (Start Menu + Desktop).
    foreach ($shortcut in @(
        (Join-Path (Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs') 'Sigil DNS.lnk'),
        (Join-Path (Join-Path $env:PUBLIC 'Desktop') 'Sigil DNS.lnk')
    )) {
        if (Test-Path -LiteralPath $shortcut) {
            Remove-Item -Force -LiteralPath $shortcut
            Write-Host "Removed $shortcut"
        }
    }

    # Remove the logon task the tray autostart registers (ignore if absent).
    & schtasks.exe /Delete /TN 'SigilDNS' /F 2>$null | Out-Null

    # Stop the tray host (a windowless sigildns-gui.exe process): it locks the
    # exe, so the install directory cannot be removed while it runs.  Wait for
    # the processes to release the image before removing the directory.
    $guiExe = Join-Path $InstallDir 'sigildns-gui.exe'
    $guiProcs = @(Get-Process -Name 'sigildns-gui' -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -eq $guiExe })
    $guiProcs | Stop-Process -Force -ErrorAction SilentlyContinue
    $guiProcs | Wait-Process -Timeout 5 -ErrorAction SilentlyContinue

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

    Write-Host 'Sigil DNS uninstalled.'
}

Assert-Elevated

$doInstall = $Install.IsPresent
$doUninstall = $Uninstall.IsPresent

if ($doInstall -and $doUninstall) {
    throw 'Pass either -Install or -Uninstall, not both.'
}

if (-not $doInstall -and -not $doUninstall) {
    Write-Host @'
 ____ ___ ____ ___ _       ____  _   _ ____
/ ___|_ _/ ___|_ _| |     |  _ \| \ | / ___|
\___ \| | |  _ | || |     | | | |  \| \___ \
 ___) | | |_| || || |___  | |_| | |\  |___) |
|____/___\____|___|_____| |____/|_| \_|____/
'@
    Write-Host ''
    Write-Host 'Sigil DNS setup'
    Write-Host '  1. Install or upgrade'
    Write-Host '  2. Uninstall'
    switch ((Read-Host 'Choose 1 or 2').Trim()) {
        '1' { $doInstall = $true }
        '2' { $doUninstall = $true }
        default { throw 'No valid choice; run with -Install or -Uninstall to skip the menu.' }
    }
    Write-Host ''
}

if ($doInstall) {
    Install-SigilDns
}
else {
    Uninstall-SigilDns
}
