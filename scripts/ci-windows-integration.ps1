# CI integration test: exercises REAL NRPT behavior end to end on a Windows
# runner (GitHub-hosted Windows runners run as admin).
#
# Flow:
#   1. start `sigildns.exe enable` as a background resolver
#   2. wait for the NRPT rule to appear
#   3. prove the system resolver now resolves via loopback
#   4. Force-kill the process (simulated kill -9): leaves dirty marker + stale lock
#   5. assert the dirty marker is set
#   6. `restore` must reclaim the stale lock and remove the NRPT rule
#   7. finally: always remove our rule / kill our process, even on failure
#
# The disruptive part is wrapped in try/finally so the runner's DNS is always
# restored.

$ErrorActionPreference = 'Stop'

$displayName = 'Sigil DNS'
$repoRoot = Split-Path -Parent $PSScriptRoot
$exe = Join-Path $repoRoot 'sigildns.exe'
$stateDir = Join-Path $env:RUNNER_TEMP 'sigildns-ci'
$state = Join-Path $stateDir 'state.json'

function Remove-OurNrptRule {
    Get-DnsClientNrptRule -ErrorAction SilentlyContinue |
        Where-Object { $_.DisplayName -eq $displayName } |
        Remove-DnsClientNrptRule -Force -ErrorAction SilentlyContinue
}

if (-not (Test-Path $exe)) {
    throw "sigildns.exe not found at $exe (build it first)"
}

if (Test-Path $stateDir) {
    Remove-Item -Recurse -Force $stateDir
}
New-Item -ItemType Directory -Force -Path $stateDir | Out-Null

$proc = $null
try {
    # Clean slate: remove any leftover rule of ours.
    Remove-OurNrptRule

    # 1. Start the resolver in the background.
    # No --listen: use the product's dual-stack default (127.0.0.1:53 + [::1]:53)
    # so it matches the NRPT rule and a ::1-first lookup can still reach us.
    $enableArgs = @(
        'enable',
        '--upstream', 'https://dns.quad9.net/dns-query',
        '--bootstrap', '9.9.9.9:53',
        '--state', $state
    )

    # 2. Start it and wait for the NRPT rule; retry a bounded number of times
    # because the initial DoH health check can transiently fail on CI runners.
    $maxAttempts = 3
    $rule = $null
    for ($attempt = 1; $attempt -le $maxAttempts; $attempt++) {
        Write-Host "starting (attempt $attempt/$maxAttempts): $exe $($enableArgs -join ' ')"
        $proc = Start-Process -FilePath $exe -ArgumentList $enableArgs -PassThru -NoNewWindow
        Write-Host "sigildns.exe pid $($proc.Id)"

        # Wait for the NRPT rule; fail fast if the process died.
        $deadline = (Get-Date).AddSeconds(30)
        while ((Get-Date) -lt $deadline) {
            if ($proc.HasExited) {
                break
            }

            $rule = Get-DnsClientNrptRule -ErrorAction SilentlyContinue |
                Where-Object { $_.DisplayName -eq $displayName }
            if ($rule) {
                break
            }

            Start-Sleep -Seconds 1
        }

        if ($rule) {
            break
        }

        if ($attempt -eq $maxAttempts) {
            if ($proc.HasExited) {
                throw "sigildns.exe exited early with code $($proc.ExitCode)"
            }
            throw "NRPT rule '$displayName' did not appear within 30s"
        }

        Write-Host "attempt $attempt did not come up (upstream may be transiently unreachable); retrying..."
        if (-not $proc.HasExited) {
            Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        }
        Remove-OurNrptRule
        Start-Sleep -Seconds 3
    }

    Write-Host "NRPT rule '$displayName' is active"

    # 3. Prove resolution through the system resolver now goes via loopback.
    ipconfig /flushdns | Out-Null
    $resolved = Resolve-DnsName example.com -Type A -DnsOnly -ErrorAction Stop
    $answer = $resolved | Where-Object { $_.IPAddress } | Select-Object -First 1
    if (-not $answer) {
        throw "Resolve-DnsName returned no address: $($resolved | Out-String)"
    }
    Write-Host "resolved example.com -> $($answer.IPAddress) (via loopback)"

    # 4. Simulated kill -9: leaves dirty marker and a stale lockfile behind.
    Stop-Process -Id $proc.Id -Force
    $proc.WaitForExit()
    Write-Host "killed sigildns.exe (pid $($proc.Id))"

    # 5. Dirty marker must be set while the resolver is no longer running.
    $statusJson = & $exe status --state $state
    if ($LASTEXITCODE -ne 0) {
        throw "status failed with exit code $LASTEXITCODE"
    }
    $st = $statusJson | ConvertFrom-Json
    if (-not $st.dirty) {
        throw "dirty marker not set after kill: $($statusJson | Out-String)"
    }
    Write-Host "dirty marker is set as expected"

    # 6. Recovery: restore must reclaim the stale lock and remove the rule.
    & $exe restore --state $state
    if ($LASTEXITCODE -ne 0) {
        throw "restore failed with exit code $LASTEXITCODE"
    }

    $leftover = @(Get-DnsClientNrptRule -ErrorAction SilentlyContinue |
        Where-Object { $_.DisplayName -eq $displayName })
    if ($leftover.Count -ne 0) {
        throw "NRPT rule '$displayName' still present after restore"
    }
    Write-Host "restore reclaimed the stale lock and removed the NRPT rule"

    $proc = $null
    Write-Host 'INTEGRATION TEST PASSED'
}
finally {
    # 7. Belt-and-suspenders: never leave the runner with a broken DNS setup.
    if ($proc -and -not $proc.HasExited) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
    }

    Get-Process -Name 'sigildns' -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue

    Remove-OurNrptRule
    ipconfig /flushdns | Out-Null
}
