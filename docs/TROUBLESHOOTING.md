# Troubleshooting

## Where things live

| Item | Windows path |
|---|---|
| Config | `%ProgramData%\dns-client\config.yaml` |
| State | `%ProgramData%\dns-client\state.json` |
| Lock | `%ProgramData%\dns-client\state.json.lock` |
| NRPT rule | Display name `dns-client`, namespace `.` |

Find the resolved config path with `dns-client config path`. The state file is
owned by the service (SYSTEM + Administrators); reading it needs no elevation
for `status`, but editing it by hand is not supported.

## Reading `status`

`dns-client status` prints the persisted state as JSON:

```json
{
  "timestamp": "2026-10-11T12:00:00Z",
  "mode": "on",
  "intercept": "nrpt",
  "active_upstream": "https://dns.quad9.net/dns-query",
  "snapshot": { "interfaces": [] },
  "last_health": { "ours": true, "ipv4": true, "ipv6": true, "detail": "ok" },
  "schema_version": 1,
  "dirty": false
}
```

| Field | Meaning |
|---|---|
| `mode` | Desired state (`on`/`off`). |
| `intercept` | Mechanism in use (`nrpt` or `adapter`). |
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
dns-client run --upstream https://dns.quad9.net/dns-query --bootstrap 9.9.9.9:53
```

## `dirty` state — recovery with `restore`

The service writes the dirty marker after applying interception and clears it
only after a clean restore. A crash, `kill`, or power loss therefore leaves
`dirty: true`; the next service start restores from the snapshot first and never
re-snapshots a loopbacked machine (which would capture `127.0.0.1` as the
"original" DNS).

To recover manually:

```powershell
dns-client status     # confirm "dirty": true
dns-client restore    # remove NRPT/interception, flush DNS, clear dirty
dns-client status     # expect "dirty": false, "mode": "off"
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
DNS can hard-break. `dns-client` logs a warning and does not modify foreign
rules:

```
conflicting NRPT rule detected; not fighting it (VPN/other DNS tool)
```

Inspect rules:

```powershell
Get-DnsClientNrptRule | Format-Table DisplayName, Namespace, NameServers
```

Only one of the VPN or `dns-client` should own DNS for a given namespace. If VPN
DNS is broken, either stop `dns-client` (`service stop`) or disconnect the VPN.

## Config parse errors

Config errors are reported on `enable`/`run`. Examples:

| Message | Fix |
|---|---|
| `parsing config: ...` | YAML syntax error; validate indentation/types. |
| `unsupported schema_version N (want 1)` | Set `schema_version: 1`. |
| `invalid mode "x" (want "on" or "off")` | Use `on` or `off`. |
| `active profile "x" not found in profiles` | `profile` must name a key under `profiles`. |
| `profile "x" has no upstreams` | Add at least one `upstreams` entry. |
| `profile "x": cache enabled with non-positive size_bytes` | Set `size_bytes > 0` or disable the cache. |
| `profile "x": invalid block_response "y"` | Use `0.0.0.0` or `nxdomain`. |
| `profile "x": per_domain N needs domains and an upstream` | Each rule needs both fields. |

If a config file is unreadable at service start, the service **still starts in a
degraded state** so `restore` and status work; enabling then fails with
`configuration unavailable: ...` until the file is fixed. Restart the service
after fixing it.

## "another dns-client instance is running"

A single-instance lock (`state.json.lock`) guards the snapshot. This error means
a live process holds it. If the holder crashed, a stale lock is reclaimed
automatically when its recorded pid is dead; `restore` will reclaim it. If it
persists, check for a leftover process:

```powershell
Get-Process dns-client
```

## Service will not start or is degraded

- `service start` timing out at the SCM's ~30 s start budget: the service
  reports Running before crash recovery/re-enable, so a slow PowerShell health
  check does not trip error 1053. A failed recovery or enable leaves the service
  **running but degraded** (log says `running degraded`); use `status` and
  `disable`.
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

If a VPN is connected, its rules may still be in effect after `dns-client` is
off — that is the VPN's, not ours.
