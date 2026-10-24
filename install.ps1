# install.ps1 - installer / uninstaller for Sigil DNS.
#
# One file, two ways to start it:
#
#   irm https://raw.githubusercontent.com/daika7ana/sigil-dns/main/install.ps1 | iex
#     downloads and verifies the latest release, then shows this menu
#
#   .\install.ps1        (in an extracted release, next to sigildns.exe)
#     installs those local files and shows the same menu
#
# Switches (skip the menu):
#   .\install.ps1 -Install [-DesktopShortcut] [-AddToPath] [-OpenGui] [-Source <sigildns.exe>] [-GuiSource <sigildns-gui.exe>]
#   .\install.ps1 -Uninstall [-RemoveData]
#   .\install.ps1 -Quiet -Install [-AddToPath] [-OpenGui]
#     unattended: no menu, no prompts; defaults to install when neither -Install
#     nor -Uninstall is given.  PATH and GUI are opt-in, never implied.
#
# Inspect before piping anything into iex:
#   irm <url> -OutFile install.ps1; notepad install.ps1
#
# Only the install/uninstall step needs admin rights; the download and checksum
# do not.  Started without elevation the script re-runs itself through UAC.

[CmdletBinding()]
param(
    [switch] $Install,
    [switch] $Uninstall,

    # Uninstall: delete the data directory (config + state) as well.
    [switch] $RemoveData,

    # Install: also create a Desktop shortcut (a Start Menu shortcut is always
    # created).
    [switch] $DesktopShortcut,

    # Unattended: skip the menu and the post-install prompts.  Defaults to
    # install when neither -Install nor -Uninstall is given.
    [switch] $Quiet,

    # Install: add the install directory to the system PATH without asking.
    [switch] $AddToPath,

    # Install: open the GUI when done without asking.
    [switch] $OpenGui,

    # Install: defaults to the sibling copy in the extracted release.
    [string] $Source,
    [string] $GuiSource,

    [string] $InstallDir = (Join-Path $env:ProgramFiles 'SigilDNS'),
    [string] $DataDir = (Join-Path $env:ProgramData 'SigilDNS')
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
# The 5.1 progress bar makes a 35 MB download several times slower.
$ProgressPreference = 'SilentlyContinue'

$repo = 'daika7ana/sigil-dns'
$serviceName = 'sigildns'

if ($Install -and $Uninstall) {
    throw 'Pass either -Install or -Uninstall, not both.'
}
if ($RemoveData -and -not $Uninstall) {
    throw '-RemoveData only applies with -Uninstall.'
}
if ($DesktopShortcut -and $Uninstall) {
    throw '-DesktopShortcut only applies when installing.'
}
if (($AddToPath -or $OpenGui) -and $Uninstall) {
    throw '-AddToPath and -OpenGui only apply when installing.'
}

function Remove-Temp {
    param([string] $Path)
    if ($Path -and (Test-Path -LiteralPath $Path)) {
        Remove-Item -LiteralPath $Path -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Test-Interactive {
    try { return -not [Console]::IsInputRedirected }
    catch { return $false }
}

function Confirm-Option {
    param(
        [Parameter(Mandatory)] [string] $Question,
        [bool] $Default = $true
    )

    # No console (service, CI, redirected input): answer No, never hang.
    if (-not (Test-Interactive)) { return $false }

    $hint = if ($Default) { '[Y/n]' } else { '[y/N]' }
    try { $answer = Read-Host "$Question $hint" }
    catch { return $false }
    if (-not $answer) { return $Default }
    return $answer.Trim() -match '^[Yy]'
}

function Update-SystemPath {
    param(
        [Parameter(Mandatory)] [string] $Entry,
        [switch] $Remove
    )

    # Edit the registry value directly: [Environment]::SetEnvironmentVariable
    # rewrites REG_EXPAND_SZ as REG_SZ and expands %SystemRoot% entries.
    $key = [Microsoft.Win32.Registry]::LocalMachine.OpenSubKey(
        'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', $true)
    if ($null -eq $key) { throw 'Cannot open the system environment registry key.' }

    try {
        $raw = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $entries = @($raw -split ';' | Where-Object { $_ })
        $present = $entries -contains $Entry

        if ($Remove) {
            if (-not $present) { return $false }
            $entries = @($entries | Where-Object { $_ -ne $Entry })
        }
        else {
            if ($present) { return $false }
            $entries += $Entry
        }

        $key.SetValue('Path', ($entries -join ';'), [Microsoft.Win32.RegistryValueKind]::ExpandString)
        return $true
    }
    finally {
        $key.Close()
    }
}

function Send-EnvironmentChange {
    # Tell Explorer the environment changed, so new terminals inherit it without
    # a sign-out.  Best-effort: the value is already written.
    try {
        if (-not ('SigilDns.NativeMethods' -as [type])) {
            Add-Type -Namespace SigilDns -Name NativeMethods -MemberDefinition @'
[DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Auto)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
'@
        }
        $result = [UIntPtr]::Zero
        [void][SigilDns.NativeMethods]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
    }
    catch {
        # Ignore; Explorer picks the value up at the next sign-in.
    }
}

function Get-LatestRelease {
    # The one-line form has no local binaries: fetch, verify, extract.
    Write-Host 'Looking up the latest Sigil DNS release...'
    try {
        $release = Invoke-RestMethod -UseBasicParsing -Headers @{ 'User-Agent' = 'sigildns-install' } `
            -Uri "https://api.github.com/repos/$repo/releases/latest"
    }
    catch {
        throw "Could not reach the GitHub release API: $($_.Exception.Message)"
    }

    $zipAsset = $release.assets | Where-Object { $_.name -match '^sigildns_.+_windows_amd64\.zip$' } | Select-Object -First 1
    $sumsAsset = $release.assets | Where-Object { $_.name -eq 'SHA256SUMS.txt' } | Select-Object -First 1
    if ($null -eq $zipAsset -or $null -eq $sumsAsset) {
        throw "Release $($release.tag_name) is missing the Windows zip or SHA256SUMS.txt."
    }

    $temp = Join-Path ([IO.Path]::GetTempPath()) ('sigildns-install-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $temp | Out-Null

    try {
        $zip = Join-Path $temp $zipAsset.name
        $sums = Join-Path $temp 'SHA256SUMS.txt'
        Write-Host "Downloading $($zipAsset.name)..."
        Invoke-WebRequest -UseBasicParsing -Uri $zipAsset.browser_download_url -OutFile $zip
        Invoke-WebRequest -UseBasicParsing -Uri $sumsAsset.browser_download_url -OutFile $sums

        # Verify before extracting: a truncated or swapped download must never run.
        $expected = $null
        foreach ($line in Get-Content -LiteralPath $sums) {
            $parts = $line.Trim() -split '\s+'
            if ($parts.Count -ge 2 -and $parts[-1].TrimStart('*') -eq $zipAsset.name) {
                $expected = $parts[0]
                break
            }
        }
        if (-not $expected) {
            throw "SHA256SUMS.txt has no entry for $($zipAsset.name)."
        }
        $actual = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash
        if ($actual -ne $expected) {
            throw "Checksum mismatch for $($zipAsset.name): expected $expected, got $actual."
        }
        Write-Host "SHA-256 verified: $actual"

        $root = Join-Path $temp 'release'
        Expand-Archive -LiteralPath $zip -DestinationPath $root
        $installer = Join-Path $root 'install.ps1'
        if (-not (Test-Path -LiteralPath $installer)) {
            # shortcut: releases before the unified installer ship
            # installer/setup.ps1; drop this fallback after the next release.
            $installer = Join-Path $root 'installer/setup.ps1'
        }
        if (-not (Test-Path -LiteralPath $installer)) {
            throw "install.ps1 is missing from $($zipAsset.name)."
        }

        return [pscustomobject]@{
            Temp      = $temp
            Exe       = Join-Path $root 'sigildns.exe'
            GuiExe    = Join-Path $root 'sigildns-gui.exe'
            Installer = $installer
        }
    }
    catch {
        Remove-Temp $temp
        throw
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

function Invoke-WithRetry {
    param(
        [Parameter(Mandatory)] [scriptblock] $Action,
        [int] $TimeoutSeconds = 10
    )

    # Windows can keep an exe mapped for a short time after its process
    # exits (and an antivirus scan can hold it too), so replacing/removing it
    # immediately after Stop-Process can still fail with "being used by
    # another process".  Retry instead of relying on a fixed sleep.
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $reported = $false
    while ($true) {
        try {
            & $Action
            return
        }
        catch {
            if ((Get-Date) -ge $deadline) { throw }
            if (-not $reported) {
                Write-Host 'Waiting for the file to be released...'
                $reported = $true
            }
            Start-Sleep -Milliseconds 200
        }
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
    $exe = $exePath
    if (-not $exe) { $exe = Join-Path $PSScriptRoot 'sigildns.exe' }
    if (-not (Test-Path -LiteralPath $exe)) {
        throw "sigildns.exe not found at '$exe'. Pass -Source <path-to-exe>."
    }

    $exeDir = Split-Path -Parent $exe
    if (-not $exeDir) { $exeDir = (Get-Location).Path }
    $gui = $guiPath
    if (-not $gui) { $gui = Join-Path $exeDir 'sigildns-gui.exe' }

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

    Write-Host "Installing $exe -> $targetExe"
    Invoke-WithRetry { Copy-Item -LiteralPath $exe -Destination $targetExe -Force -ErrorAction Stop }

    $guiInstalled = Test-Path -LiteralPath $gui
    if ($guiInstalled) {
        # A running GUI or windowless tray host locks the installed exe.  Wait
        # for the processes to release the image before copying over it.
        $guiProcs = @(Get-Process -Name 'sigildns-gui' -ErrorAction SilentlyContinue |
            Where-Object { $_.Path -eq $guiExe })
        $guiProcs | Stop-Process -Force -ErrorAction SilentlyContinue
        $guiProcs | Wait-Process -Timeout 5 -ErrorAction SilentlyContinue

        Write-Host "Installing $gui -> $guiExe"
        Invoke-WithRetry { Copy-Item -LiteralPath $gui -Destination $guiExe -Force -ErrorAction Stop }
    }
    else {
        Write-Warning "sigildns-gui.exe not found at '$gui'; installing the service + CLI only."
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

    $addToPath = $AddToPath.IsPresent
    if (-not $Quiet -and -not $addToPath) {
        $addToPath = Confirm-Option -Question "Add '$InstallDir' to the system PATH (all users)?"
    }
    if ($addToPath) {
        try {
            if (Update-SystemPath -Entry $InstallDir) {
                Send-EnvironmentChange
                Write-Host "Added to PATH; open a new terminal to run 'sigildns'."
            }
            else {
                Write-Host "'$InstallDir' is already on PATH."
            }
        }
        catch {
            Write-Warning "Could not update PATH: $($_.Exception.Message)"
        }
    }

    if ($guiInstalled) {
        $openGui = $OpenGui.IsPresent
        if (-not $Quiet -and -not $openGui) {
            $openGui = Confirm-Option -Question 'Open Sigil DNS now?'
        }
        if ($openGui) {
            Start-Process -FilePath $guiExe
            Write-Host 'Opened Sigil DNS.'
        }
    }

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

    # Remove the install dir from the system PATH if it was added.
    try {
        if (Update-SystemPath -Entry $InstallDir -Remove) {
            Send-EnvironmentChange
            Write-Host "Removed $InstallDir from PATH"
        }
    }
    catch {
        Write-Warning "Could not update PATH: $($_.Exception.Message)"
    }

    # Stop the tray host (a windowless sigildns-gui.exe process): it locks the
    # exe, so the install directory cannot be removed while it runs.  Wait for
    # the processes to release the image before removing the directory.
    $guiExe = Join-Path $InstallDir 'sigildns-gui.exe'
    $guiProcs = @(Get-Process -Name 'sigildns-gui' -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -eq $guiExe })
    $guiProcs | Stop-Process -Force -ErrorAction SilentlyContinue
    $guiProcs | Wait-Process -Timeout 5 -ErrorAction SilentlyContinue

    if (Test-Path -LiteralPath $InstallDir) {
        Invoke-WithRetry { Remove-Item -Recurse -Force -LiteralPath $InstallDir -ErrorAction Stop }
        Write-Host "Removed $InstallDir"
    }

    if ($RemoveData) {
        if (Test-Path -LiteralPath $DataDir) {
            Invoke-WithRetry { Remove-Item -Recurse -Force -LiteralPath $DataDir -ErrorAction Stop }
            Write-Host "Removed data directory $DataDir"
        }
    }
    else {
        Write-Host "Kept data directory $DataDir (pass -RemoveData to delete config and state)."
    }

    Write-Host 'Sigil DNS uninstalled.'
}

# A copy next to the binaries installs those; the piped one-liner (or a script
# saved by itself) has no local payload and fetches the latest release.
$here = $null
if ($PSCommandPath) { $here = Split-Path -Parent $PSCommandPath }
$exePath = $Source
$guiPath = $GuiSource
if (-not $exePath -and $here) {
    $sibling = Join-Path $here 'sigildns.exe'
    if (Test-Path -LiteralPath $sibling) {
        $exePath = $sibling
        if (-not $guiPath) { $guiPath = Join-Path $here 'sigildns-gui.exe' }
    }
}
$hasLocalPayload = [bool]$exePath

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
$isAdmin = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $isAdmin) {
    if ($hasLocalPayload) {
        if (-not $PSCommandPath) {
            throw 'Run install.ps1 as a file (not through iex) when using -Source.'
        }

        # Relaunch through UAC, forwarding the caller's parameters verbatim, so
        # the menu appears in an elevated window.
        $relaunch = @('-NoProfile', '-ExecutionPolicy', 'Bypass')
        if (-not $Quiet) { $relaunch += '-NoExit' }
        $relaunch += @('-File', "`"$PSCommandPath`"")
        foreach ($name in @('Install', 'Uninstall', 'RemoveData', 'DesktopShortcut', 'Quiet', 'AddToPath', 'OpenGui', 'Source', 'GuiSource', 'InstallDir', 'DataDir')) {
            if (-not $PSBoundParameters.ContainsKey($name)) { continue }
            $value = $PSBoundParameters[$name]
            if ($value -is [switch]) {
                if ($value.IsPresent) { $relaunch += "-$name" }
            }
            else {
                $relaunch += "-$name"
                $relaunch += "`"$value`""
            }
        }
        $proc = $null
        try {
            if ($Quiet) {
                # Unattended: wait so the caller sees the real exit code.
                $proc = Start-Process powershell.exe -Verb RunAs -PassThru -Wait -ArgumentList $relaunch
            }
            else {
                Start-Process powershell.exe -Verb RunAs -ArgumentList $relaunch
            }
        }
        catch {
            if ($Quiet) { throw }
            Write-Host "Elevation was cancelled or refused: $($_.Exception.Message)" -ForegroundColor Red
            return
        }
        if ($Quiet -and $null -ne $proc -and $null -ne $proc.ExitCode -and $proc.ExitCode -ne 0) {
            throw "Installer failed with exit code $($proc.ExitCode)."
        }
        return
    }

    # Remote: download and verify first, then run the extracted copy elevated.
    # -Wait keeps the download alive until the installer finishes.
    $release = Get-LatestRelease
    try {
        # Forward only what the caller chose: with no switch the elevated copy
        # shows the Install / Uninstall menu.
        $flags = @()
        if ($Install) { $flags += '-Install' }
        if ($Uninstall) { $flags += '-Uninstall' }
        if ($RemoveData) { $flags += '-RemoveData' }
        if ($DesktopShortcut) { $flags += '-DesktopShortcut' }

        # shortcut: a release before the unified installer ships setup.ps1,
        # which knows no -Quiet/-AddToPath/-OpenGui; drop with the fallback.
        $legacy = (Split-Path -Leaf $release.Installer) -eq 'setup.ps1'
        if (-not $legacy) {
            if ($Quiet) { $flags += '-Quiet' }
            if ($AddToPath) { $flags += '-AddToPath' }
            if ($OpenGui) { $flags += '-OpenGui' }
        }
        elseif ($Quiet -and -not $Install -and -not $Uninstall) {
            $flags += '-Install'
        }
        if ($legacy -and ($AddToPath -or $OpenGui)) {
            Write-Warning "-AddToPath/-OpenGui need the updated installer; this release's setup.ps1 ignores them."
        }

        foreach ($name in @('InstallDir', 'DataDir')) {
            if ($PSBoundParameters.ContainsKey($name)) {
                $flags += "-$name"
                $flags += "'" + $PSBoundParameters[$name].Replace("'", "''") + "'"
            }
        }

        # The launcher keeps the window open only on failure so the error is
        # readable; -Quiet must not wait for a keypress.
        $launcher = Join-Path $release.Temp 'run-installer.ps1'
        $escapedInstaller = $release.Installer.Replace("'", "''")
        $pause = if (-not $Quiet) { "    Read-Host 'Press Enter to close'" }
        @"
try {
    & '$escapedInstaller' $($flags -join ' ')
}
catch {
    Write-Host `$_.Exception.Message -ForegroundColor Red
$pause
    exit 1
}
"@ | Set-Content -LiteralPath $launcher -Encoding utf8

        Write-Host 'Requesting administrator rights (UAC)...'
        try {
            $proc = Start-Process -FilePath 'powershell.exe' -Verb RunAs -PassThru -Wait `
                -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$launcher`"")
        }
        catch {
            throw "Elevation was cancelled or refused: $($_.Exception.Message)"
        }
        if ($null -ne $proc -and $null -ne $proc.ExitCode -and $proc.ExitCode -ne 0) {
            throw "Installer failed with exit code $($proc.ExitCode)."
        }
    }
    finally {
        Remove-Temp $release.Temp
    }
    return
}

$doInstall = $Install.IsPresent
$doUninstall = $Uninstall.IsPresent

if (-not $doInstall -and -not $doUninstall) {
    if ($Quiet -or -not (Test-Interactive)) {
        # Unattended (quiet, CI, service): install.
        $doInstall = $true
    }
    else {
        Write-Host @'
 ____ ___ ____ ___ _       ____  _   _ ____
/ ___|_ _/ ___|_ _| |     |  _ \| \ | / ___|
\___ \| | |  _ | || |     | | | |  \| \___ \
 ___) | | |_| || || |___  | |_| | |\  |___) |
|____/___\____|___|_____| |____/|_| \_|____/
'@
        Write-Host ''
        Write-Host 'Sigil DNS setup'
        Write-Host 'System-wide encrypted DNS for Windows 11.'
        Write-Host ''
        Write-Host '  1. Install or upgrade'
        Write-Host '  2. Uninstall'
        Write-Host '  3. Exit'
        switch ((Read-Host 'Choose 1, 2 or 3').Trim()) {
            '1' { $doInstall = $true }
            '2' { $doUninstall = $true }
            default {
                Write-Host 'Nothing changed.'
                return
            }
        }
        Write-Host ''
    }
}

$temp = $null
try {
    if ($doInstall -and -not $hasLocalPayload) {
        $release = Get-LatestRelease
        $temp = $release.Temp
        $exePath = $release.Exe
        $guiPath = $release.GuiExe
    }

    if ($doInstall) {
        Install-SigilDns
    }
    else {
        Uninstall-SigilDns
    }
}
finally {
    if ($temp) { Remove-Temp $temp }
}
