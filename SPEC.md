# SPEC — System-Wide Encrypted DNS for Windows 11

Name: **Sigil DNS** (formerly dns-client).
Target: Windows 11 x64 (ARM64 deferred).
Developed on Linux, cross-built, released on a Windows runner.
**License: GPLv3** — forced by `AdguardTeam/urlfilter`; see §3.

Status: the design below is implemented and shipped through **v0.3.0**. §11
separates what exists from what is deliberately deferred.

---

## 1. Goal

A Windows app giving the user system-wide encrypted DNS (DoH/DoT/DoQ) to a
chosen provider (Quad9, Cloudflare, NextDNS, ControlD, custom), with per-domain
routing rules and blocklists — the practical feature set of YogaDNS, without a
kernel driver.

### Non-goals
- Kernel WFP/NDIS driver interception.
- Defeating app-level DoH/DoT, VPN-pushed DNS, or hardcoded resolver IPs.
- macOS/Linux clients; authoritative DNS serving.
- Per-adapter DNS rewriting (the contingency in §4.2 is not implemented).

### What is / isn't intercepted
| Scenario | Intercepted? |
|---|---|
| Normal apps using the OS resolver | **Yes** |
| Apps with hardcoded resolver IPs / own stub resolver | **No** |
| Browsers with built-in Secure DNS (DoH) | **No** — the user must disable it in-browser |
| VPN that installs NRPT rules (e.g. `{.}`) | **Partial** — VPN rules may override or break ours; VPN DNS may hard-break |
| Captive portal (pre-auth) | **Partial** — bootstrap DoH fails until we back off |
| hosts file / LLMNR / mDNS / NetBIOS local resolution | **No** — bypasses DNS entirely |
| DNS-level blocklists | Only see OS-resolver traffic |

The GUI and README surface this table; it is a product boundary, not a bug.

---

## 2. Architecture

```
                     Windows DNS Client (svchost dnscache / dnsrslvr.dll)
                                      │
                    ┌─────────────────┴──────────────────┐
                    │  Interception (see §4)             │
                    │  NRPT rule (all names)             │
                    │  (adapter fallback not built, §4.2)│
                    └─────────────────┬──────────────────┘
                                      ▼
             127.0.0.1:53 / [::1]:53  (UDP + TCP)
   ┌────────────────────────  Sigil DNS service  ──────────────────────────┐
   │  runs as LocalSystem (§4.7)                                           │
   │  dnsproxy.Proxy ──► urlfilter DNSEngine ──► per-domain routing ──► DoH │
   │      ▲                    (blocklists)          (dnsmasq rules)  upstream│
   └──────┬────────────────────────────────────────────────────────────────┘
          ▲  named pipes (SDDL-locked) — versioned request/response
          │  control (admin) + status (read-only)
   ┌──────┴──────────────┐
   │  sigildns-gui.exe   │  manifest-elevated (§15.1); reads sanitized status
   └─────────────────────┘  over the read-only pipe, writes via --apply
```

- **Service**: elevated, owns the resolver, config, interception lifecycle, and
  state file. Runs as **LocalSystem**. The originally-planned least-privilege
  virtual account `NT SERVICE\sigildns` is infeasible: it cannot create pipe
  instances under the pipe SDDL, is locked out of its own ACLed state file, and
  lacks the administrator rights that NRPT writes require.
  Least privilege remains a future hardening goal (service SID + explicit
  grants) once those operations can be delegated.
- **GUI**: reads sanitized status over the read-only status pipe; state-changing
  operations go through the elevated `--apply` helper. The GUI code never reads
  `config.yaml` directly. Built with **go-gui** (§15). The optional system-tray
  icon runs in a separate native host process started with `--tray`, so the GUI
  can exit while the icon stays; the `tray` build tag remains a retired stub.
- **Binaries**: one Go module produces two Windows executables —
  `sigildns.exe` (console subsystem: CLI, service host, `--apply` helper) and
  `sigildns-gui.exe` (GUI subsystem: the config client). A single exe cannot be
  both without a stray console or lost stdout. Both embed the
  `requireAdministrator` manifest (§15.1).

---

## 3. Stack (locked)

| Concern | Choice | License |
|---|---|---|
| Language | Go 1.27.3 | — |
| DNS engine | `github.com/AdguardTeam/dnsproxy` v0.86.0 (embedded) | Apache-2.0 |
| Wire/types | `github.com/miekg/dns` v1.1.73 | BSD-3 |
| Filtering | `github.com/AdguardTeam/urlfilter` v0.23.4 (`DNSEngine`) | **GPL-3.0** → whole app GPLv3 |
| Windows service | `golang.org/x/sys/windows/svc` | BSD-3 |
| IPC | `github.com/Microsoft/go-winio` v0.6.4 (deliberately pinned) | MIT |
| GUI + tray | `github.com/go-gui-org/go-gui` v0.89.1 (`SystemTrayCfg`) | MIT |
| Config | YAML (`gopkg.in/yaml.v3`) | MIT/Apache |
| State | JSON (stdlib), atomic write, ACL SYSTEM+Admins | — |

**Licensing note:** static Go linking of urlfilter (GPLv3) makes the shipped
binary a combined work → the project is GPLv3. Documented in LICENSE and
README. Do **not** assume "run urlfilter as a separate process" avoids
copyleft.

---

## 4. Interception & Windows integration

### 4.1 Primary mechanism: NRPT
Add one rule covering all namespaces; restore = delete the rule:

```
Add-DnsClientNrptRule -Namespace "." -NameServers 127.0.0.1,::1
```

Pros: one rule covers every current **and future** adapter; a single restore
action; no per-adapter snapshot churn.

Caveats (handled):
- Rules persist across reboot → the dirty marker triggers recovery on the next
  start. Our rule is tagged (DisplayName/GUID) so we can find and remove exactly
  ours.
- VPN/clients may install equal-or-more-specific NRPT rules that take
  precedence (VPN DNS may hard-break). Conflicting foreign rules are detected
  and warned about in the log/UI, never modified.
- Per-domain **bypass** rules are more-specific NRPT rules with **no name
  servers** (an exemption), so those names fall back to the interface's normal
  DNS (see docs/LIMITATIONS.md).

### 4.2 Fallback mechanism (NOT IMPLEMENTED)
The shipped build implements NRPT only. The per-adapter approach is kept here
as a contingency in case NRPT proves unreliable in the field; it is not
selectable, not persisted, and not wired into enable/disable, drift, or
stop/uninstall verification. Adding it is a feature, not a switch:

- Capture the pre-change servers **before** touching anything, keyed by stable
  interface **GUID** (`InterfaceIndex` is not stable across reconnects/reboots).
- Set **both** families per active interface: `Set-DnsClientServerAddress
  -InterfaceIndex N -ServerAddresses 127.0.0.1` / `::1`. Set both or IPv6 leaks;
  any adapter not loopbacked is a leak window.
- Re-apply on adapter changes (§4.6), restore on disable/uninstall, and make the
  drift probe and stop/uninstall verification adapter-aware — today they check
  NRPT only.

### 4.3 Enable sequence
Strict order:
1. Bind UDP+TCP listeners on `127.0.0.1:53` and `[::1]:53`. If bind fails →
   abort, report the owner.
2. **Two-stage health check** (§9) — prove loopback→us, then prove upstream
   (per family) with an uncached query.
3. Persist state + write the atomic **dirty marker** (schema-versioned).
4. Apply interception (NRPT rule).
5. `ipconfig /flushdns`.

Never touch NRPT until the resolver is proven listening *and* forwarding. The
"keep the user online" guarantee must not depend on rollback succeeding.

### 4.4 State / restore hygiene
- **GUID is stable; InterfaceIndex is not** — relevant only to the future
  adapter fallback (§4.2).
- Atomic write (temp + fsync + rename), with a **schema version** field.
- The NRPT build stores **no per-interface snapshot**: restore is deleting our
  tagged rule. The dirty marker records that interception may still be applied
  (crash recovery), not a snapshot.
- If the adapter fallback is ever implemented: **never overwrite a snapshot
  while dirty** — restore from the persisted snapshot first, then re-enable from
  a clean baseline. Re-snapshotting a loopbacked system would capture
  `127.0.0.1` as "original" and lose the user's real DNS.
- Restore is **idempotent**, survives partial failure (keeps dirty on failure),
  and must work **even if the config YAML is corrupt**.
- Per-family state differs (IPv4 DHCP vs IPv6 static is common);
  IPv6-disabled interfaces are skipped; a `::1` bind failure must not abort
  enabling. (Relevant when the adapter fallback is implemented.)
- Uninstall/upgrade while enabled must restore first.

### 4.5 Port 53 conflicts
Pre-bind-test **UDP and TCP** on both loopbacks *before* any interception
change. Detect the owning process (common: `SharedAccess`/ICS from
Hyper-V/WSL/Docker/HNS; also a Docker backend or another resolver). Offer to stop
ICS; never silently fail. Do not disable the Windows DNS Client service — it is
mandatory on 24H2.

### 4.6 Drift detection
`NotifyIpInterfaceChange` fires only on adapter up/down — it **misses** GPO
refresh (~90 min), MDM/Intune, VPN, and other DNS tools rewriting servers. Add:

- a registry watcher (`RegNotifyChangeKeyValue` on
  `HKLM\...\Tcpip(6)\Parameters\Interfaces\*\NameServer`), and
- a **45 s reconcile loop** comparing expected-vs-actual. After 3 consecutive
  failed re-applies it auto-disables and stops the service (never leave it
  running unprotected). If the auto-disable itself fails, the resolver stays up
  and the loop keeps retrying, because stopping would point DNS at a dead port.

### 4.7 Security of the IPC path
Unelevated→SYSTEM DNS control is a privilege path (any local process could
redirect auth/corp traffic). Therefore:

- **Named pipes only** (no local HTTP), split by privilege:
  - **Control pipe** `\\.\pipe\sigildns` — state-changing operations. SDDL
    `D:P(A;;GA;;;SY)(A;;GA;;;BA)` (SYSTEM + Administrators only).
  - **Status pipe** `\\.\pipe\sigildns-status` — read-only. SDDL
    `D:P(A;;GA;;;SY)(A;;GRGW;;;AU)` (SYSTEM plus Authenticated Users with
    GR+GW, which go-winio needs to dial). Served with a read-only authorizer, so
    `enable`/`disable` are rejected even though any local user can connect. The
    status payload is a sanitized DTO that omits `active_upstream` (resolver id)
    and interface GUIDs.
- Per-method authorization is enforced in the server as defence in depth, in
  addition to the pipe SDDL. There is no implicit authorizer: every listener
  names one explicitly.
- The status pipe rejects admin methods; the control pipe rejects malformed or
  unauthorized ones.
- **Service account**: runs as **LocalSystem**, not the least-privilege virtual
  account `NT SERVICE\sigildns` (see §2).
- State file **and its directory** are ACLed to SYSTEM + Administrators with
  the Windows security APIs (`SetNamedSecurityInfo`; well-known SIDs,
  locale-independent). ACL failures are returned, not swallowed.
- **Path hardening**: the service, the elevated helper, and the CLI resolve the
  machine-wide paths from the `ProgramData` **KnownFolder API**
  (`config.SystemPath` / `SystemStatePath`), never the `%ProgramData%`
  environment variable, and never fall back to a user-writable directory. An env
  override cannot redirect the ACL'd config/state to a weaker ACL, and the
  helper and service always agree on the same path.
- **Server robustness**: each request is handled under a panic recovery
  (`safeHandle` returns a generic `ErrInternal` rather than killing the service),
  and the connection write deadline is armed *after* the handler runs — a
  slow-but-successful `reload` is not misread as `EOF` by the client.

### 4.8 WSL
WSL2 (NAT) may bypass loopback DNS. Documented techniques: `dnsTunneling` in
`.wslconfig`, or mirrored networking. Not automated.

---

## 5. Engine configuration (dnsproxy)

- Listeners: UDP + TCP on `127.0.0.1:53` and `[::1]:53` (plain DNS — Windows
  apps cannot speak DoQ to us; DoQ/DoT/DoH are **upstream** protocols only).
- Upstream protocols: **DoH, DoH3, DoT, and DoQ** (`quic://host:853`, handled by
  dnsproxy via quic-go). DoH3 is HTTP/3-only DoH (`h3://host/dns-query`, UDP/443)
  with no TCP fallback; it requires a provider that serves HTTP/3. DoQ needs
  **UDP/853**, often blocked on restrictive
  networks → fall back to DoT/DoH. DoQ hostnames: Quad9 `dns.quad9.net:853`,
  ControlD `quic://pN.freedns.controld.com` / `<id>.dns.controld.com`, NextDNS
  `quic://<id>.dns.nextdns.io:853`.
- Default upstream pool + per-domain upstreams via
  `proxy.UpstreamConfig.DomainReservedUpstreams` /
  `SpecifiedDomainUpstreams` / `SubdomainExclusions`.
- Fallback: the active profile's `fallback_profile` builds a second
  `proxy.UpstreamConfig` wired to `proxy.Config.Fallbacks`; dnsproxy tries it per
  query only after every primary upstream fails. Only that profile's
  `upstreams`/`bootstrap` are used, and fallback chains are not followed.
- **Integration pitfalls (verified against pinned dnsproxy v0.86.0):**
  - There is no `BeforeRequestHandler`. `Config.RequestHandler` **replaces**
    `DefaultHandler`; the handler must call `p.Resolve` exactly once
    (double-forwarding trap) or short-circuit.
  - Filtering runs **before** the cache (within `Resolve`) — correct order;
    AdGuard Home's `internal/dnsforward` pipeline is the reference.
  - **Flush the dnsproxy cache on every state change** (profile/upstream/rule/
    blocklist change, disable) or stale answers outlive edits. `clear_cache`
    also flushes the Windows DNS cache.
  - Hand-built blocked / `$dnsrewrite` responses are **not cached** — set
    TTL/RA/flags yourself.
  - The `[/domain/]upstream` text parser lives in dnsproxy's `internal/cmd`
    (not importable) → we maintain our own parser for `#`, `//`, wildcards, and
    DS semantics (AdGuard Home's is the reference).
- Cache: bounded size; optimistic mode deferred.

---

## 6. Filtering (urlfilter) — GPLv3

- Build `filterlist.RuleStorage` from configured lists (hosts + AdGuard/ABP
  syntax); lists are user-supplied (defaults disabled).
- `urlfilter.NewDNSEngine(storage)` → `MatchRequest` before forwarding.
- Blocked response: `0.0.0.0` / `::` (configurable) or NXDOMAIN. `$dnsrewrite`
  is supported by the engine; there is no dedicated UX.
- List updates: **on-start + manual** — the admin `refresh_lists` IPC method
  re-downloads the active profile's lists (CLI `refresh`, GUI "Update
  blocklists"), rebuilds the engine, and flushes the cache (§5). No scheduled
  updater. Changed-content dedupe is an unconditional GET compared by content
  checksum (sha256), not ETag.

---

## 7. Providers (presets)

Presets are **dual-stack** (IPv4 + IPv6) and each exposes: filtering variant,
protocol (DoH/DoH3/DoT/**DoQ**), bootstrap mode, and profile/client id. Endpoints were
verified against provider documentation (2026-10).

### 7.1 Quad9 — 4 variants (all validate DNSSEC since 2026-06-15)
| Variant | Filtering | DoH | DoT/DoQ host | IPv4 | IPv6 |
|---|---|---|---|---|---|
| Secure (recommended) | malware block | `https://dns.quad9.net/dns-query` | `dns.quad9.net:853` | `9.9.9.9`, `149.112.112.112` | `2620:fe::fe`, `2620:fe::9` |
| No Threat Blocking | none | `https://dns10.quad9.net/dns-query` | `dns10.quad9.net:853` | `9.9.9.10`, `149.112.112.10` | `2620:fe::10`, `2620:fe::fe:10` |
| Secure + ECS | malware block + ECS | `https://dns11.quad9.net/dns-query` | `dns11.quad9.net:853` | `9.9.9.11`, `149.112.112.11` | `2620:fe::11`, `2620:fe::fe:11` |
| No Blocking + ECS | none + ECS | `https://dns12.quad9.net/dns-query` | `dns12.quad9.net:853` | `9.9.9.12`, `149.112.112.12` | `2620:fe::12`, `2620:fe::fe:12` |

- `9.9.9.10` means *No Threat Blocking*, not "unsecured": DNSSEC is on for every
  endpoint. `9.9.9.11`/`9.9.9.12` add ECS.
- DoH-by-IP works (certs include service IPs) → bootstrap-free. ECS variants leak
  a network prefix: labelled clearly, never the default.
- `dns9` is a legacy alias — not shipped.

### 7.2 Cloudflare — 3 variants (id-less)
| Variant | Filtering | DoH / DoQ host | DoT host | Bootstrap (v4 + v6) |
|---|---|---|---|---|
| Standard (default) | none (1.1.1.1) | `cloudflare-dns.com` | `1dot1dot1dot1.cloudflare-dns.com` | `1.1.1.1`, `1.0.0.1`; `2606:4700:4700::1111`, `2606:4700:4700::1001` |
| Security | malware block (1.1.1.2) | `security.cloudflare-dns.com` | `security.cloudflare-dns.com` | `1.1.1.2`, `1.0.0.2`; `2606:4700:4700::1112`, `2606:4700:4700::1002` |
| Family | malware + adult (1.1.1.3) | `family.cloudflare-dns.com` | `family.cloudflare-dns.com` | `1.1.1.3`, `1.0.0.3`; `2606:4700:4700::1113`, `2606:4700:4700::1003` |

- **No resolver id** (public preset): nothing is passed in the URL.
- DoH, DoH3, DoT, and DoQ are all supported; DoQ reuses the DoH host, and DoH3
  reuses the DoH URL path with the `h3://` scheme.
- All three variants configure both bootstrap families.

### 7.3 NextDNS — Ultralow vs Anycast
`<id>` = profile id; an optional device name is the DoH path `/<id>/<device>`.

- **Ultralow (default, preferred):** `https://dns.nextdns.io/<id>`, DoT
  `<id>.dns.nextdns.io:853`. **Requires connecting by hostname with NO bootstrap
  IP** — supplying any bootstrap pins you to anycast (per NextDNS staff). Cost:
  one system-resolver lookup at startup (chicken-and-egg) to resolve the DoH
  hostname.
- **Anycast (bootstrap-pinned):** force via `dns1`/`dns2` hostnames; bootstrap v4
  `45.90.28.0`/`45.90.30.0`, v6 `2a07:a8c0::`/`2a07:a8c1::`. Use when the system
  resolver must be avoided. (Legacy v6 embeds the id: `2a07:a8c0::<id-split>`.)
- **Forced edge server — not supported by the provider.** It is a DNS-rewrite
  workaround (`ipv4-anycast.dns1.nextdns.io` →
  `ipv4-<server>-<N>.edge.nextdns.io`); NextDNS staff call it "technically
  wrong" and say technical hostnames should not be used directly. It is not a
  first-class toggle. Escape hatch: advanced users may set a custom upstream
  plus a name-override rule — unsupported.
- `dns1.nextdns.io/<id>` and `ultralow.*`/`anycast.*` hostnames are
  forum-reported, not provider-documented; only
  `https://dns.nextdns.io/<id>` is published.

### 7.4 ControlD — custom + free presets
- **Custom:** DoH `https://dns.controld.com/<id>` (DoH-by-IP works), DoT
  `<id>.dns.controld.com`; v4 `76.76.2.22`/`76.76.10.22`, v6
  `2606:1a40::22`/`2606:1a40:1::22`. Client name: `/<id>/<name>`.
- **Free:** `https://freedns.controld.com/p0…p3` (p0 unfiltered, p1 malware, p2
  ads/tracking, p3 social), DoT `pN.freedns.controld.com`; v4
  `76.76.2.11`/`76.76.10.11`, v6 `2606:1a40::11`/`2606:1a40:1::11`.
- **Named free presets** (`x-oisd`, `x-stevenblack`, `x-adguard`, `family`,
  `uncensored`) and the open-ended `x-hagezi` family share the p0 bootstrap IPs.
  **Never ship a shared/preset id as the user's own.** The custom DoQ hostname
  is inferred (`<id>.dns.controld.com`), not documented.

### 7.5 Dual-stack requirement (all presets)
- The resolver listens on `127.0.0.1:53` **and** `[::1]:53`.
- Every preset configures **both** IPv4 and IPv6 endpoints and **both** bootstrap
  families. (NextDNS `ultralow` is the deliberate no-bootstrap exception.)
- The health check runs **per family** (§9); an IPv6-disabled host must not abort
  enable (skip v6, warn).
- `EnableEDNSClientSubnet` is **off** by default (only explicit `*ECS` Quad9
  variants enable it).

### 7.6 Custom
User-entered DoH/DoH3/DoT URL plus optional bootstrap (v4/v6). Raw upstream escape
hatch for power users.

---

## 8. Config schema

```yaml
schema_version: 1
profile: default      # active profile (many stored, one active at a time)
log:                  # optional; off by default
  level: off          # off | error | info | debug
  path: ""            # empty = <ProgramData>\SigilDNS\sigildns.log
  max_size_mb: 10
  keep_mb: 3
profiles:
  default:
    upstreams: ["https://dns.quad9.net/dns-query"]
    bootstrap: [9.9.9.9, 149.112.112.112]
    fallback_profile: backup   # optional; tried only if all upstreams fail
    per_domain:
      - domains: ["corp.example"]   # exact or *.wildcard; dnsmasq semantics (§5)
        upstream: "tls://10.0.0.1"
        enabled: true               # optional; omitted = enabled
      - domains: ["printer.lan"]
        bypass: true                # resolve via normal interface DNS
    cache: { enabled: true, size_bytes: 67108864 }
    filtering:
      enabled: true
      block_response: 0.0.0.0       # or nxdomain
      block_ttl: 3600
      lists:
        - url: https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt
          enabled: true
```

State file (JSON, service-owned, ACL SYSTEM+Admins, atomic, schema-versioned):
interception mode, dirty marker, active profile/upstream, health result, and
bypass domains. **Single-instance guard**: `state.json.lock`; a lock left by a
dead pid is reclaimed, and an unreadable lock is reclaimed after a short wait
because the pid is written immediately after creation.

The full field reference, validation rules, and worked examples live in
docs/CONFIG.md.

---

## 9. Lifecycle

- **Service start**: if the dirty marker is set → **restore interception first**,
  then **always enable**. The NRPT build stores no adapter snapshot; dirty means
  a previous session may still have our rule applied. The SCM state stays
  **StartPending** (checkpointed every 10 s) until recovery and enable finish;
  only then does it become Running. A running service means interception is on —
  there is no running-but-off state. If recovery or enable fails the service
  stops rather than keep serving a half state; a failed restore leaves the dirty
  marker for the next start. A configuration error exits cleanly so the SCM does
  not restart a hopeless boot.
- **Enable**: §4.3 strict order (bind → two-stage health check → state+dirty →
  apply → flush). If apply fails, restore whatever was partially applied and stop
  the resolver; if the restore also fails, keep the resolver up (DNS must not
  point at a dead port) and leave the dirty marker.
- **Disable**: restore (delete the NRPT rule) → **flush DNS** → stop resolver →
  clear dirty marker. A failed restore leaves the resolver running so a retry is
  possible.
- **Reload**: disable (restore) → enable from the new config, with
  last-known-good rollback. If the rollback also fails the service stops; if the
  disable cannot remove interception, the resolver stays up and the service keeps
  running instead of stranding DNS.
- **Two-stage health check**: (a) query a random synthetic name **only our
  handler answers** (proves loopback→us, not "something on :53"); (b) query a
  random uncached name **per upstream family** (proves upstream, catches
  IPv6-dead-while-IPv4-ok). IPv6 failure alone warns; IPv4 failure aborts.
- **Crash/kill**: the dirty marker forces restore on the next start. SCM failure
  actions restart the service (5 s, 10 s, 30 s).
- **Exit**: always restore; flush.
- **Uninstall is ordered and fail-closed** (`uninstallPlan`/`uninstallSequence`):
  restore interception → stop the service → **verify** interception is actually
  down (a positive NRPT probe, with one local config-independent restore retry) →
  only then delete. It never deletes while interception is unproven down, so an
  uninstall can never strand a machine with interception still applied.

---

## 10. Build, packaging, signing

- Dev (Linux) uses the Docker wrapper `./dev/go` (the host has no Go toolchain):
  `./dev/go build ./...`, `./dev/go test ./...`, `./dev/go vet ./...`, and
  `GOOS=windows GOARCH=amd64 ./dev/go build ./...` for cross-compile checks.
  Adapter/NRPT/service/tray behaviour is verified on Windows CI.
- GUI is **go-gui** (pure Go, no cgo/WebView2/Node — §15), gated behind a `gui`
  build tag so the Linux `go build ./...` stays lean.
- **Two binaries** from the one module:
  - `sigildns.exe` — console subsystem, no `gui` tag: CLI, service host,
    `--apply` helper.
  - `sigildns-gui.exe` — `-tags gui -ldflags "-H windowsgui"`: the config
    client, still `--apply`-capable.
- **Release builds run on `windows-latest`**. CI runs `go build`, `vet`, and
  `test` on Linux + Windows, a GUI smoke test, a `-race` run, manifest-resource
  freshness, and the Windows NRPT integration job.
- Signing: OV code-signing certificate (EV is only needed for a kernel driver,
  which we do not ship), GH Actions + `signtool`, gated on repository secrets
  (`WINDOWS_CERT_PFX_BASE64`, `WINDOWS_CERT_PASSWORD`). Without them the
  artifacts are unsigned and SmartScreen warns.
- Ship: both binaries, `install.ps1`, `config.example.yaml`, README,
  SPEC, LICENSE, docs/, and `SHA256SUMS.txt` beside the zip. Releases publish
  draft-first (create draft → upload → undraft) so a failed run never exposes an
  assetless release. Notes come from `release-notes/<tag>.md` when present, else
  generated notes; the curated CHANGELOG entry is the reference for the same
  version.

---

## 11. Status and roadmap

**Delivered through v0.3.0:** the walking skeleton, YAML config + provider
presets (DoH/DoT/DoQ), per-domain rules + blocklists, the LocalSystem service
with dual SDDL-locked pipes, drift reconcile + config-independent restore,
packaging/signing, the five-page go-gui config client, and the opt-in tray with
`ONLOGON` autostart. Details in §15.4.

**Deferred / future:**
- Per-adapter DNS fallback (§4.2) — only if NRPT proves unreliable in the field.
- ARM64; macOS/Linux clients; kernel WFP/NDIS interception.
- Optimistic cache; concurrent multi-profile resolution (many profiles stored,
  one active).
- Scheduled list updates; a rule-editor UI beyond the current GUI forms;
  `$dnsrewrite` UX.
- Least-privilege service account (service SID + explicit grants).

---

## 12. Verification

- **Unit / platform**: config parse + validation, preset expansion,
  single-instance guard, rule selection, blocked responses, allowlist overrides,
  cache flush, IPC privilege table (non-admin rejection), uninstall ordering
  (restore → stop → verify → delete).
- **Integration (Windows CI)**: SCM install/start/stop; status DTO reflects
  state; drift re-applied and the reconcile loop catches a simulated registry
  change; two-stage health check; real Quad9 DoH through
  `scripts/ci-windows-integration.ps1`.
- **Release**: package contents (both binaries + docs + installer), checksum
  match, optional Authenticode verification; a red or still-running CI run
  blocks publishing.
- **Security**: a planted id never appears in `config_summary` JSON or any log;
  a non-admin cannot connect to the control pipe; `reload` swaps live
  resolution; declining launch-time UAC starts nothing; a tampered staging file
  is rejected with no write; a non-admin process cannot read `config.yaml`; a
  squatted status pipe is detected as unverified; `disable` works with a corrupt
  config.

---

## 13. Risks / open questions

- **Bypass/leak surface** (§1) — accepted. WinDivert (LGPLv3) or a WFP driver
  are the only ways to close it.
- **NRPT interference** — VPN/GPO/MDM rules can override ours; handled by
  detection + warning, not by fighting foreign rules.
- **`dnsproxy` API is version-sensitive** — pinned; verify `Config`/handler
  fields against the pinned tag's source, not docs.
- **NextDNS Ultralow vs Anycast** (§7.3) — ultralow (better routing) needs a
  system-resolver bootstrap lookup; anycast is bootstrap-pinned but worse
  routing. User-visible choice.
- **NextDNS forced edge** (§7.3) — unsupported by the provider; documented as an
  unsupported escape hatch only.
- **Quad9 ECS variants / captive portals** — ECS leaks a prefix (opt-in only);
  captive-portal back-off is implemented but remains a rough edge.
- **GPLv3 obligations** — ship LICENSE + source; documented in README.
- **go-gui is pre-1.0** — pin (`v0.89.1`), update deliberately. The GUI is a
  client, so swapping the shell does not touch the service core.

---

## 14. Why not just wrap dnscrypt-proxy?
dnscrypt-proxy / AdGuard Home / NextDNS CLI share the same *architecture*
(loopback resolver). We build because the ask is **service + GUI + per-domain
rules + blocklists with one-click provider switching and clean restore** —
integration they do not ship. We reuse the hardened engine (`dnsproxy`) rather
than reimplement it.

---

## 15. GUI — go-gui

**Toolkit: `github.com/go-gui-org/go-gui` (MIT), pinned `v0.89.1`.** Chosen over
Wails and verified by a hello-world spike cross-built on Linux and run on Win11
24H2.

- Rationale: pure Go — **no cgo, no WebView2, no Node/npm**; builds with the
  existing Docker wrapper (`CGO_ENABLED=0 GOOS=windows GOARCH=amd64`); cgo is
  needed only for the macOS Metal backend, which is irrelevant here.
- Rejected: Wails v2 (needs Node + the WebView2 runtime), Fyne/systray
  (cgo/MinGW), and a hand-rolled `Shell_NotifyIcon` tray — the icon uses
  go-gui's built-in `SystemTrayCfg` / `SetSystemTray`, so it adds no dependency
  and needs no extra cgo.
- Risk: go-gui is **pre-1.0** and releases frequently. Pin the version and
  update deliberately.

### 15.1 Client model
The GUI is a **client of the service** and contains no DNS logic; it links the
existing `internal/*` packages. It is a **full-parity configuration client**,
not a status viewer.

- **Reads** — the status pipe (`AU`, read-only) carries *sanitized* methods:
  `info` (protocol/app version, capability bits) and `config_summary` (active
  provider/variant/protocol **labels**, `id_set` bool, active profile name,
  bootstrap IP literals, rule/list **counts** and list **hosts**, filtering +
  cache settings, `config_valid` with a secret-free error, a revision token, and
  a `profiles` list — one sanitized `ProfileSummary` per profile: name, active
  flag, provider/variant/protocol labels, `id_set`, filtering/cache flags,
  cache size, counts, bootstrap IP literals, fallback profile name, per-list
  **hosts** (+ enabled) and per-domain rule **domains** (+ enabled, `bypass`
  flag); never ids or upstream URLs). `id_set` is provider-aware
  (`providers.ExtractID`), so DoT/DoQ ids are detected too, not only the DoH-path
  heuristic. The provider catalog is rendered **locally** from
  `internal/providers` (no IPC, no secrets).
- **Writes** — a **one-shot elevated helper**: the GUI re-launches the *same
  signed binary* `--apply <staging>` via `ShellExecuteW "runas"`. The helper
  re-loads and validates the staged patch with `internal/config`, writes
  `config.yaml` (atomic + ACL), then drives the service over the existing admin
  control pipe (`reload` / `enable` / `disable` / `refresh_lists` /
  `clear_cache`).
  The allowlist is the **whole config surface**: `ActiveProfile`, per-profile
  `Profiles` patches (provider / variant / protocol / id / custom URL /
  bootstrap / fallback profile / filtering / cache, plus **per-list operations**
  — toggle/add/remove existing lists by index — and **per-domain rules** with a
  per-rule `enabled` flag and a kind: an upstream rule, or a **bypass** rule that
  carries no upstream), the structural `AddProfile` / `RenameProfile` /
  `DeleteProfile`, and the service actions `service_install` /
  `service_uninstall` / `service_start` / `service_stop` / `refresh_lists` /
  `clear_cache`. A start re-reads the SCM registration and refuses unless
  `BinaryPathName` still points at the current executable with `--service`; the
  CLI and the GUI both use `service.Start`, so neither can bypass the check.
  Every field is validated (bootstrap as IP literals, provider / variant /
  protocol against the registry, id charset + length, explicit-zero rejection);
  ids travel only inside the DPAPI-sealed staging body, and a blank id means
  *keep* (replace-only). `refresh_lists` and `clear_cache` are both classified
  `Admin` in the §15.3 privilege table (the status pipe rejects them).
  `clear_cache` has no input: it clears the running resolver's dnsproxy cache and
  then the Windows DNS cache, so a newly added rule (for example a provider-side
  bypass) also applies to names that are already cached. No persistent broker and
  no second privileged surface. On the manifest-elevated build the GUI process is
  *already* elevated, so a write no longer raises its own UAC prompt — consent is
  per **launch**, not per action — but the helper remains the sole writer and the
  only path that touches staged input.
- **Surface** — five pages (left nav rail: Status, Settings, Rules, Logging,
  About).
  - **Status**: a dashboard — a state hero with an illustration, then
    Connection Info, Filtering, Filter lists, and Cache & bootstrap cards
    (stacking at narrow widths); the header keeps only the **Restore now**
    action for a crash-leftover dirty state.
  - **Settings**: a service card — a **Start / Stop** control (Start runs the
    service and applies interception, Stop removes it; the installed/running
    state is the header badge) plus install / remove — an **Appearance** card
    with the Auto / Light / Dark theme picker, a profile list with add /
    rename / delete / set-active, and a per-profile editor (provider / variant /
    protocol, masked id, custom URL, bootstrap, fallback profile, cache + size).
  - **Rules**: the filtering toggle and block response beside the filter-list
    editor, and the per-domain routing rules, in one **scrollable** column —
    per-list and per-rule enable toggles, add / remove, a `*.` wildcard hint, and
    an "Update filter lists" action that drives the admin `refresh_lists` method
    (offered only while the service is running).
  - **Logging**: opt-in service file logging — a level selector (off / errors
    only / normal / verbose) and a file path (blank = default beside
    `config.yaml`) with a **Browse** picker and an **Apply** that takes effect at
    the **next service start**, plus a live read-only tail of the log file with a
    0–30 s refresh slider (0 = off) and **Open log file** / **Clear log file**
    actions.
  - **About**: three cards — identity, **Version & license** (client version,
    live service version, interface version, license and its GPL-3.0 linkage
    note), and **Links** (author and repository, both opened in the default
    browser).
  A `gui.lock` beside `gui.json` enforces a single GUI instance.
- **Tray (opt-in)** — Settings → **Window & tray**: a **System tray icon**
  switch (off by default) plus, while it is on, independent **Minimize to tray**
  and **Close to tray** switches. The icon lives in a **separate native host
  process** (`sigildns-gui.exe --tray`, no window and no OpenGL), so the GUI
  exits when closed or minimized to the tray instead of keeping a ~230 MB
  GPU-backed process alive; the host costs ~25-30 MB. Single or double click
  launches the GUI, or brings its window to front when it is already open;
  right click offers a disabled **Service: …** status row, **Open Sigil DNS**,
  **Open log file**, **Clear DNS cache** (resolver cache + Windows DNS cache),
  and **Exit**. If the host cannot be started, the switch reverts with a toast
  instead of leaving a dead setting; the host refreshes the status row from a
  live read every 5 s and retries icon creation while the shell is starting.
  This uses go-gui's tray backend in the host process, not the retired `tray`
  build tag.
- **Identity** — before trusting reads or sending writes, verify the pipe server
  *is* the service: `GetNamedPipeServerProcessId(conn)` == the SCM-reported
  `ProcessId`. Unverified → flag the UI as untrusted and refuse writes (kills
  pipe-squatting without a nonce).
- **Config is single-writer**: only the elevated helper writes `config.yaml`; the
  service never does. Reload semantics are in §9.

**Elevation and autostart (deliberate user decisions).** The shipped Windows
binary embeds an application manifest (`cmd/sigildns/sigildns.manifest`) with
`<requestedExecutionLevel level="requireAdministrator" uiAccess="false"/>`.
Windows therefore shows a UAC prompt on **every** launch and the process runs
elevated for its lifetime. This overrides the "no persistent elevated process"
clause of §15.3.8; the rest of the security model is unchanged — the one-shot
`--apply` helper still works, the status pipe stays read-only/sanitized, the
control pipe stays SYSTEM+Administrators only, service identity is still
verified, and no silent/auto elevation is introduced by the manifest itself.
Because there is a single CLI binary, **all** invocations (`status`,
`providers list`, `--gui`, …) prompt UAC, not just the GUI. In effect the
per-write consent gate is replaced by per-launch consent: the GUI holds an admin
token for its whole lifetime, while the helper's re-validation, field allowlist,
and ACL-enforced atomic write are unchanged.

The opt-in **Start tray icon on startup** setting is the one further exception:
it registers a per-user Task Scheduler `ONLOGON` task (`SigilDNS`, task XML with
a logon trigger and principal bound to this user, `InteractiveToken` and
`HighestAvailable`) that launches `sigildns-gui.exe --tray`. The GUI is already
elevated, so it creates the task without a prompt, and the task starts the tray
host elevated at sign-in **with no UAC prompt**. The XML also keeps the task startable
on battery and removes the default 72-hour execution limit, neither of which
`schtasks` flags can express. The task is per-user, created only by an explicit
setting, carries no resolver ids or config data, and is removed when the
setting/tray is disabled and by uninstall. Its exe path is pinned to whatever
binary the setting was enabled from (enable it from the installed copy). It
starts the tray host only, never a window; it never applies config or starts the
service, and it does not change the control/status pipe, the helper, or any
per-action consent rule —
every config write still goes through the one-shot `--apply` helper.

### 15.2 Sensitive data
- Resolver ids live only in the admin-only `%ProgramData%\SigilDNS\config.yaml`
  and, for status, the equally ACLed `state.json` (SYSTEM + Administrators).
  They are **never** on the status pipe, never logged, and never on
  argv/environment (argv is cross-process readable). The single documented
  exception is the `run`/`enable --upstream` dev/CI flag; the normal paths
  (`config.yaml`, the GUI helper, `providers show --id-file`) keep ids off argv.
  The GUI *code* never reads `config.yaml` directly — full-fidelity reads happen
  only inside the elevated helper, regardless of whether the GUI process itself
  is elevated.
- Editing is **replace-only** (empty id field, or an empty custom-upstream URL
  field, = keep); display is masked (`••••`); no reveal on the AU pipe.
- `--apply` staging: `%LOCALAPPDATA%\SigilDNS\staging\<uuid>.bin`, **DPAPI**
  user-scope, deleted after read.
- `%LOCALAPPDATA%\SigilDNS\gui.json` (user scope) holds window geometry, theme,
  last selection (no id), and a cached last status — never secrets, never an
  authorization input.
- Concurrency: `config_summary` carries a revision token (**mtime + size**, *not*
  a file hash — ids are low-entropy and a hash is brute-forceable); the helper
  aborts on a revision mismatch.
- **Logs** (opt-in, default off): the file lives beside `config.yaml` (SYSTEM +
  Administrators). A redacting `slog.Handler`, seeded from the active profile's
  resolver ids (`providers.ExtractIDs`, refreshed on every reload), scrubs
  DoH-path and DoT/DoQ hostname-label ids, URL userinfo/query, and `sdns://` at
  **every** level; `debug` additionally records query names and upstream
  traffic — a deliberate decision (§15.3.10), not a leak. Level/path changes
  apply at the next service start.

### 15.3 Security red lines
1. Control pipe stays `D:P(A;;GA;;;SY)(A;;GA;;;BA)` — **never** `AU`.
2. Status pipe stays read-only forever (`ReadOnlyAuthorizer` +
   `SanitizeStatus`); every new method is classified read-only/admin in one table
   with a test asserting non-admin rejection.
3. **No AU-reachable config or DNS mutation** (no "validate for me" write
   surface).
4. Named pipes only — **no local HTTP/TCP control channel**.
5. Never send across any pipe: ids, credential-bearing URLs (upstream/user/list),
   or hashes of secret-bearing files. Only labels, flags, counts, hosts, and
   non-secret IP literals.
6. Never secrets on argv/env (`run`/`enable --upstream` is the only documented
   dev/CI exception, §15.2); staging is DPAPI + user ACL + delete.
7. The elevated helper never trusts staged input: re-load, re-validate, allowlist
   fields, fixed config path, atomic + ACL write; ACL failure is a hard error.
8. **No silent elevation** (no scheduled task / COM broker / "highest
   privileges" shortcut, no persistent elevated process, no auto-apply).
   *Exceptions (user decisions, see §15.1): the shipped binaries embed a
   `requireAdministrator` manifest, so they are elevated for their lifetime and
   prompt on every manual launch; the opt-in **Start tray icon on startup**
   setting registers a per-user `ONLOGON` task at `HighestAvailable`, so the tray
   host starts elevated at sign-in without UAC. It is removed when the setting or tray
   is disabled and by uninstall.*
9. `config.yaml` and `state.json` remain SYSTEM+Administrators; user-scope files
   never hold secrets.
10. Never log credentials: the redacting `slog.Handler` (seeded from
    `providers.ExtractIDs`, re-seeded on reload) scrubs resolver ids, URL
    userinfo/query, and `sdns://` at every level; it is the audited choke point.
    *User decision:* `debug` intentionally records query names and upstream
    traffic (the log file stays ACLed SYSTEM + Administrators; the GUI labels
    `debug` as sensitive).
11. All config writes go through `internal/config`; `restore` stays
    config-independent so a corrupt config can never brick recovery.
12. Verify service identity before trusting reads or sending actions.

### 15.4 Delivered phases
Status: **phases 0–8 are implemented.**

0. **Preconditions** — ACL `config.yaml` (+ dir); upstreams redacted from logs;
   non-secret profile metadata (`provider`/`variant`/`protocol`).
1. **IPC extension** — `info` + `config_summary` (status pipe, with the
   `profiles` list), `reload` (control pipe), privilege table + tests.
2. **go-gui shell** — window, status polling, identity check,
   degraded/stopped/unverified states, `gui.json`, and the `gui.lock`
   single-instance guard.
3. **Profile view + draft editing** — local catalog, masked id, local validation,
   and writes through the elevated helper.
4. **Elevation + Apply** — the allowlist is the full config surface plus service
   actions, with DPAPI staging, the `--apply` helper, per-launch UAC UX, and
   revision-conflict abort.
5. **Rules/lists editing + `refresh_lists`** — a dedicated scrollable **Rules**
   page with the filter-list editor (add / remove / per-list enable, addressed
   by index so list URLs never round-trip) and per-domain routing rules
   (domains, kind upstream or bypass, replace-only upstream by `Keep` index,
   per-rule `enabled`, `*.` wildcard hint); `refresh_lists` re-downloads lists
   live (GUI "Update filter lists", CLI `refresh`).
6. **Enable/disable/degrade UX** — **Start / Stop** in the Settings service
   card, service management via the helper, the dirty **Restore now** header
   action, and the five-page surface.
7. **Packaging** — go-gui binary (no cgo), installer ships the GUI + Start-Menu
   shortcut, and a protocol-version mismatch is surfaced from `info`.
   Code signing stays optional, gated on repository secrets.
8. **Tray** — an opt-in go-gui system-tray icon (off by default) in a separate
   native host process, with a live service status row, Open, Open log file,
   Clear DNS cache (resolver + Windows caches), Exit, and independent
   close-to-tray / minimize-to-tray switches; a host that cannot be started
   reverts the setting; optional `ONLOGON` autostart.

**Polling model (CPU/power).** While the window is focused, the Status page reads
the live service state — SCM state + status DTO only, never `config.yaml` —
about every 1 s, and polling pauses while the window is unfocused/minimized. A
minimized window is detected with `IsIconic` on the 1 s tick, so **Minimize to
tray** can quit it to the host; quitting pauses/ends the poller. The tray host
refreshes its status row from a live read every 5 s, independent of the window,
so a service-state change reaches the menu within about five seconds. The full
config summary is fetched at startup, after the
user's own edits, on a page switch, and on a 30 s safety tick. An unchanged read
pushes nothing and requests no repaint, so the on-demand render loop stays
asleep when idle.

Build: `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build` (no cgo anywhere). The
GUI lives behind a `gui` build tag so the Linux `go build ./...` stays lean; the
retired `tray` tag is unrelated. The `--apply` helper mode is always present and
headless.

### 15.5 Verification
A planted id never appears in `config_summary` JSON or any log; a non-admin
cannot connect to the control pipe; `reload` swaps live resolution (dig via
loopback); declining the launch-time UAC prompt starts nothing; a tampered
staging file is rejected with no write; a non-admin process cannot read
`config.yaml` (the manifest-elevated GUI still never reads it directly — writes
go through the helper); a squatted `status` pipe is detected as unverified;
`disable` works with a corrupt config. The `profiles` summary carries no ids or
URLs (test); an unknown config key is rejected rather than ignored; the helper
rejects an out-of-registry provider/variant/protocol, a non-IP bootstrap, a bad
id charset, a malformed per-domain domain or upstream, an explicit-zero cache
size while the cache is enabled, and a zero block TTL; uninstall never
deletes while interception is unproven down (ordered restore → stop → verify →
delete, unit-tested).
