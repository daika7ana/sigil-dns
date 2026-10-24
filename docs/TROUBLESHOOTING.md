# Troubleshooting

## "Windows protected your PC" on first run

Releases are unsigned, so SmartScreen has no reputation for the binaries and
warns once per new file hash. Verify the zip against `SHA256SUMS.txt` on the
release page, then choose **More info → Run anyway**. If the button is missing,
group policy or Smart App Control is blocking it: allow the file hash in
Defender, or ask IT. A signature is the only signal that names the publisher —
the hash only proves the download is intact.

The `.ps1` inside the zip carries the same Mark-of-the-Web, so under the default
`RemoteSigned` policy PowerShell refuses to run `install.ps1` ("not digitally
signed"). Override the policy for this run, or unblock the extracted folder and
rerun:

```powershell
powershell -ExecutionPolicy Bypass -File .\install.ps1
Get-ChildItem -Recurse | Unblock-File
```

Double-clicking a `.ps1` starts a **non-elevated** shell (or opens it in an
editor, depending on the association). `install.ps1` detects that and relaunches
itself through UAC, so double-click works; if it opened in an editor, use
right-click → **Run with PowerShell**. A group policy that forbids elevation
(`ConsentPromptBehaviorAdmin`) blocks both — an elevated prompt started by IT is
then required.

## Where things live

| Item | Windows path |
|---|---|
| Config | `%ProgramData%\SigilDNS\config.yaml` |
| State | `%ProgramData%\SigilDNS\state.json` |
| Lock | `%ProgramData%\SigilDNS\state.json.lock` |
| NRPT rule | Display name `Sigil DNS`, namespace `.` |

Find the resolved config path with `sigildns config path`. The state file is
owned by the service (SYSTEM + Administrators). `sigildns status` reads it
through the service/binary, which requests admin at launch, so the command
prompts UAC like every other invocation; editing the file by hand is not
supported.

## Reading `status`

`sigildns status` prints the persisted state as JSON:

```json
{
  "timestamp": "2026-10-11T12:00:00Z",
  "mode": "on",
  "intercept": "nrpt",
  "active_upstream": "https://dns.quad9.net/dns-query",
  "last_health": { "ours": true, "ipv4": true, "ipv6": true, "detail": "ok" },
  "schema_version": 1,
  "dirty": false
}
```

| Field | Meaning |
|---|---|
| `mode` | Persisted interception state (`on`/`off`); a running service is always `on`. |
| `intercept` | Mechanism in use (currently always `nrpt`). |
| `active_upstream` | The upstream(s) captured at enable time. |
| `dirty` | `true` means interception was applied but not cleanly restored — run `restore`. |
| `last_health` | Two-stage health result: `ours` (loopback is us), `ipv4`/`ipv6` (upstream per family), `detail` (human text). |

If the service has never been enabled, `status` prints
`state: none (<path>)` and exits 0.

`dirty: true` is the key signal. It is set once interception is applied and
cleared only after a successful restore.

## Port 53 is in use — `enable` fails to bind

Symptom (from `enable`/`run`, or the service log):

```
binding DNS listeners on [127.0.0.1:53 [::1]:53] (another process may hold port
53, e.g. Windows ICS/SharedAccess from Hyper-V/WSL/Docker): ...
```

The most common owner is `SharedAccess` (Internet Connection Sharing), started
by Hyper-V, WSL, Docker, or HNS. Find the owner:

```powershell
Get-NetTCPConnection -LocalPort 53 -State Listen |
    Select-Object LocalAddress, LocalPort, OwningProcess
Get-Process -Id <OwningProcess>
```

If it is ICS and you do not need Hyper-V/WSL/Docker NAT:

```powershell
Stop-Service SharedAccess
Set-Service  SharedAccess -StartupType Manual   # optional, prevents restart
```

> Do **not** stop the Windows DNS Client service to free the port; it is
> mandatory on Windows 11 24H2 and stopping it breaks resolution.

`enable` aborts before touching interception when the bind fails, so your
existing DNS is untouched.

## Health check fails

`enable` returns `health check failed: <detail>`. The detail names the stage and
address:

- `stage A <addr>: not ours (<err>)` — something else answered on the loopback
  address, or our resolver is not actually the one bound there. Check the
  port-53 conflict above. Stage A queries a random synthetic name that only our
  handler answers, precisely to catch "some other process is on :53".
- `stage B <addr>: upstream unreachable (<err>)` — the loopback resolver works
  but the upstream does not. Causes: wrong upstream URL, no network yet, a
  firewall/IPS blocking 443/853, or a DoQ endpoint with UDP/853 blocked.

Stage B runs per family. IPv6-upstream failure alone does **not** abort enabling
(IPv4 is required; a dead IPv6 path is surfaced in the detail). If IPv4 is down,
enabling aborts.

Quick manual check:

```powershell
# Is our resolver answering?
Resolve-DnsName example.com -Server 127.0.0.1 -Type A
# Does the upstream work at all?
sigildns run --upstream https://dns.quad9.net/dns-query --bootstrap 9.9.9.9:53
```

## `dirty` state — recovery with `restore`

The service writes the dirty marker after applying interception and clears it
only after a clean restore. A crash, `kill`, or power loss therefore leaves
`dirty: true`. The build stores no adapter snapshot: the next service start
deletes our NRPT rule first (if present), then re-enables from a clean baseline.

To recover manually:

```powershell
sigildns status     # confirm "dirty": true
sigildns restore    # remove NRPT/interception, flush DNS, clear dirty
sigildns status     # expect "dirty": false, "mode": "off"
```

`restore` is idempotent, can reclaim a stale lock left by a dead process, and
works even if the config YAML is corrupt (it reads only the state file). On
partial failure it **keeps** the dirty marker set; fix the underlying problem
and run `restore` again.

If `enable` finds a dirty state it also restores first, so recovery is implicit
on the next enable.

## NRPT conflicts with a VPN

A VPN (or another DNS tool) may install a catch-all or more-specific NRPT rule.
Windows gives more specific rules precedence, so ours can be overridden and VPN
DNS can hard-break. Sigil DNS logs a warning and does not modify foreign
rules:

```
conflicting NRPT rule detected; not fighting it (VPN/other DNS tool)
```

Inspect rules:

```powershell
Get-DnsClientNrptRule | Format-Table DisplayName, Namespace, NameServers
```

Only one of the VPN or Sigil DNS should own DNS for a given namespace. If VPN
DNS is broken, either stop Sigil DNS (`service stop`) or disconnect the VPN.

## Config parse errors

Config errors are reported on `enable`/`run`. Examples:

| Message | Fix |
|---|---|
| `parsing config: ...` | YAML syntax error; validate indentation/types. |
| `unsupported schema_version N (want 1)` | Set `schema_version: 1`. |
| `active profile "x" not found in profiles` | `profile` must name a key under `profiles`. |
| `profile "x" has no upstreams` | Add at least one `upstreams` entry. |
| `profile "x": cache enabled with non-positive size_bytes` | Set `size_bytes > 0` or disable the cache. |
| `profile "x": invalid block_response "y"` | Use `0.0.0.0` or `nxdomain`. |
| `profile "x": per_domain N needs domains` | Add at least one domain to the rule. |
| `profile "x": per_domain N needs an upstream` | An upstream rule needs a non-empty `upstream` (a bypass rule needs none). |
| `profile "x": per_domain N is a bypass rule with an upstream` | A `bypass: true` rule must not set `upstream`. |

A `bypass: true` rule is intentional: those domains are resolved by the system's
normal DNS, not by Sigil DNS — see [LIMITATIONS](LIMITATIONS.md).

If a config file is unreadable at service start, crash recovery (`restore`) still
runs, then the service **stops** with `configuration unavailable: ...` in the
log. Fix the file, then start the service again. A running service always has
interception on.

## "another Sigil DNS instance is running"

A single-instance lock (`state.json.lock`) guards the state file. This error means
a live process holds it. If the holder crashed, a stale lock is reclaimed
automatically when its recorded pid is dead; `restore` will reclaim it. A lock
whose pid cannot be read is reclaimed too, after a short wait — the pid is
written immediately after the lock is created, so an unreadable lock has no live
writer. If the error still persists, delete `state.json.lock` by hand and check
for a leftover process:

```powershell
Get-Process sigildns
```

## Service will not start or keeps stopping

- `service start` timing out at the SCM's ~30 s start budget: the service stays
  in **StartPending** and checkpoints every 10 s while crash recovery and
  enable run, so a slow PowerShell health check does not trip error 1053.
  Running is reported only after interception is on. If recovery or enable
  fails, the service **stops** (log says `service start failed`) — it is never
  left running with interception off. A bad config exits cleanly, without the
  SCM's restart loop, so fix the file and start it again.
- Recovery actions are configured to restart the service on failure (5 s, 10 s,
  30 s). A loop of restarts usually means a port-53 conflict or an unreachable
  upstream.

## Nothing resolves after disable

`disable` stops the resolver, removes the NRPT rule, flushes DNS, and clears the
dirty marker. If name resolution is still broken, check whether a foreign NRPT
rule or an adapter override remains:

```powershell
Get-DnsClientNrptRule | Format-Table DisplayName, Namespace, NameServers
ipconfig /flushdns
```

If a VPN is connected, its rules may still be in effect after Sigil DNS is
off — that is the VPN's, not ours.
