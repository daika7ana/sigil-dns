# SPEC — System-Wide Custom DNS Client for Windows 11

Working name: **dns-client** (rename later).
Target: Windows 11 x64 (ARM64 deferred).
Developed on Linux, cross-built, released/signed on a Windows runner.
**License: GPLv3** (forced by `AdguardTeam/urlfilter`; see §3). Full project is therefore GPLv3.

---

## 1. Goal

A Windows app giving the user system-wide encrypted DNS (DoH/DoT) to a chosen provider
(ControlD, Quad9, NextDNS, custom), with per-domain routing rules and blocklists — the practical
feature set of YogaDNS, without a kernel driver.

### Non-goals (v1)
- Kernel WFP/NDIS driver interception (phase 2 only, if ever).
- Defeating app-level DoH/DoT, VPN-pushed DNS, or hardcoded resolver IPs.
- macOS/Linux clients; authoritative DNS serving.

### What is / isn't intercepted (surface this table in the tray, not only docs)
| Scenario | Intercepted? |
|---|---|
| Normal apps using the OS resolver | ✅ Yes |
| Apps with hardcoded resolver IPs / own stub resolver | ❌ No |
| Browsers with built-in Secure DNS (DoH) | ❌ No — user must disable in-browser |
| **Any adapter not currently loopbacked** (new/transiently-reverted/missed) — parallel multi-homed queries leak | ❌ Leak window |
| VPN that installs NRPT rules (e.g. `{.}`) | ⚠️ VPN rules may **override or break** our rule; VPN DNS hard-breaks |
| Captive portal (pre-auth) | ⚠️ Bootstrap DoH fails → user can't reach the portal until we back off |
| hosts file / LLMNR / mDNS / NetBIOS local resolution | ❌ Bypasses DNS entirely |
| DNS-level blocklists | Only see OS-resolver traffic |

---

## 2. Architecture

```
                     Windows DNS Client (svchost dnscache / dnsrslvr.dll)
                                      │
                    ┌─────────────────┴──────────────────┐
                    │  Interception (see §4)             │
                    │  PRIMARY: NRPT rule (all names)    │
                    │  FALLBACK: per-adapter DNS = loopbk │
                    └─────────────────┬──────────────────┘
                                      ▼
             127.0.0.1:53 / [::1]:53  (UDP + TCP)
   ┌────────────────────────  dns-client service  ────────────────────────┐
   │  runs as LocalSystem (v1 deviation; see §4.7)                          │
   │  dnsproxy.Proxy ──► urlfilter DNSEngine ──► per-domain routing ──► DoH  │
   │      ▲                    (blocklists)          (dnsmasq rules)  upstream│
   └──────┬────────────────────────────────────────────────────────────────┘
          ▲  named pipes (SDDL-locked) — versioned request/response
          │  control (admin) + status (read-only)
   ┌──────┴──────┐
   │  Tray GUI   │  unelevated; reads status over the read-only pipe
   └─────────────┘
```

- **Service**: elevated, owns resolver + config + interception lifecycle + state file.
  Run as **LocalSystem** for v1. The originally-planned least-privilege virtual
  account `NT SERVICE\dns-client` is infeasible: it cannot create pipe instances
  under the pipe SDDL, is locked out of its own state file by `state.secure()`,
  and lacks the administrator rights that NRPT (`Add-DnsClientNrptRule`) and
  adapter DNS writes require. Least privilege remains a future hardening goal
  (service SID + explicit grants) once those operations can be delegated.
- **Tray**: unelevated user process; reads status over the read-only status pipe; state-changing ops prompt UAC.
- One Go module; one binary with `--service` / `--tray` / `--cli` modes.

---

## 3. Stack (locked)

| Concern | Choice | License |
|---|---|---|
| Language | Go 1.26+ | — |
| DNS engine | `github.com/AdguardTeam/dnsproxy` (embedded) | Apache-2.0 |
| Wire/types | `github.com/miekg/dns` | BSD-3 |
| Filtering | `github.com/AdguardTeam/urlfilter` (`DNSEngine`) | **GPL-3.0** → whole app GPLv3 |
| Windows service | `golang.org/x/sys/windows/svc` | BSD-3 |
| Tray GUI | `fyne.io/systray` (maintained fork; Apache-2.0), **build-tagged** | Apache-2.0 |
| Config | YAML (`gopkg.in/yaml.v3`) | MIT/Apache |
| State/snapshot | JSON (stdlib), atomic write, ACL SYSTEM+Admins | — |

**Licensing note:** static Go linking of urlfilter (GPLv3) makes the shipped binary a combined work
→ the project is GPLv3, full stop. Documented in repo LICENSE + README. Do **not** assume "run
urlfilter as a separate process" avoids copyleft.

---

## 4. Interception & Windows integration

### 4.1 PRIMARY mechanism: NRPT
Add one rule covering all namespaces; restore = delete the rule:
```
Add-DnsClientNrptRule -Namespace "." -NameServers 127.0.0.1,::1
```
Pros: one rule covers every current **and future** adapter; a single restore action; no per-adapter
snapshot churn. 
Caveats (must handle):
- Rules persist across reboot → same "dirty" recovery problem; tag our rule (DisplayName/GUID) so
  we can find/remove exactly ours.
- VPN/clients may install equal-or-more-specific NRPT rules that take precedence (VPN DNS may
  hard-break). Detect conflicting rules; surface a warning in the tray.
- **Must be verified on Win11 24H2 during phase 1** before committing; if unreliable, fall back.

### 4.2 FALLBACK mechanism: per-adapter loopback
Only if NRPT proves unreliable. Per active interface (IPv4 **and** IPv6):
apply `Set-DnsClientServerAddress -InterfaceIndex N -ServerAddresses 127.0.0.1` / `::1`.
Set **both** families or IPv6 leaks. New/changed adapters re-applied by the watcher (§4.6).

### 4.3 Enable sequence (corrected — health check BEFORE flipping)
Strict order:
1. Bind UDP+TCP listeners on `127.0.0.1:53` and `[::1]:53`. If bind fails → abort, report owner.
2. **Two-stage health check** (§9) — prove loopback→us, then prove upstream (per family) with an
   uncached query.
3. Persist snapshot + write atomic **dirty marker** (schema-versioned).
4. Apply interception (NRPT rule, or per-adapter).
5. `ipconfig /flushdns`.

Never touch adapters/NRPT until the resolver is proven listening *and* forwarding. The "keep the
user online" guarantee must not depend on rollback succeeding.

### 4.4 Snapshot / restore hygiene
- **GUID is stable; InterfaceIndex is not.** Store GUIDs; resolve to indexes at restore time.
- Atomic write (temp + fsync + rename), **schema version** field.
- **Never overwrite a snapshot while dirty.** If dirty: **restore from the persisted snapshot
  first**, then re-enable from a clean baseline. Re-snapshotting a loopbacked system captures
  `127.0.0.1` as "original" and loses the user's real DNS forever.
- Restore is **idempotent**, must survive partial failure (keep dirty on failure), and must work
  **even if the config YAML is corrupt**.
- Per-family state differs (IPv4 DHCP vs IPv6 static is common); IPv6-disabled interfaces skipped;
  `::1` bind failure must not abort enabling.
- Uninstall/upgrade while enabled must restore first (MSI custom action).

### 4.5 Port 53 conflicts
Pre-bind-test **UDP and TCP** on both loopbacks *before* any interception change. Detect the owning
process (common: `SharedAccess`/ICS from Hyper-V/WSL/Docker/HNS; also Docker backend, other
resolvers). Offer to stop ICS; never silently fail. Do not disable the Windows DNS Client service
(mandatory on 24H2).

### 4.6 Drift detection
`NotifyIpInterfaceChange` fires only on adapter up/down — it **misses** GPO refresh (~90 min),
MDM/Intune, VPN, and other DNS tools rewriting servers. Add:
- a registry watcher (`RegNotifyChangeKeyValue` on `HKLM\...\Tcpip(6)\Parameters\Interfaces\*\NameServer`), and
- a **30–60 s reconcile loop** comparing expected-vs-actual; re-apply, or auto-disable + warn on
  persistent drift.

### 4.7 Security of the IPC path
Unelevated→SYSTEM DNS control is a privilege path (any local process could redirect auth/corp
traffic). Therefore:
- **Named pipes only** (no local HTTP), split by privilege:
  - **Control pipe** `\\.\pipe\dns-client` — state-changing ops. SDDL
    `D:P(A;;GA;;;SY)(A;;GA;;;BA)` (SYSTEM + Administrators only).
  - **Status pipe** `\\.\pipe\dns-client-status` — read-only. SDDL
    `D:P(A;;GA;;;SY)(A;;GRGW;;;AU)` (SYSTEM plus Authenticated Users with
    GR+GW, which go-winio needs to dial). Served with a read-only authorizer, so
    `enable`/`disable` are rejected even though any local user can connect. The
    status payload is a sanitized DTO that omits `active_upstream` (resolver id)
    and interface GUIDs.
- Per-method authorization is enforced in the server as defence in depth, in
  addition to the pipe SDDL. There is no implicit authorizer: every listener
  names one explicitly.
- Tray needs no UAC to read status (read-only pipe); toggling prompts **UAC**.
- **Service account**: v1 runs as **LocalSystem**, not the least-privilege
  virtual account `NT SERVICE\dns-client` (see §2 for why).
- State file **and its directory** are ACLed to SYSTEM + Administrators via
  `SetSecurityInfo` (well-known SIDs, locale-independent). ACL failures are
  returned, not swallowed.

### 4.8 WSL
WSL2 (NAT) may bypass loopback DNS. Document (`dnsTunneling`/`.wslconfig`). Not automated in v1.

---

## 5. Engine configuration (dnsproxy)

- Listeners: UDP + TCP on `127.0.0.1:53` and `[::1]:53` (plain DNS — Windows apps cannot speak DoQ
  to us; DoQ/DoT/DoH are **upstream** protocols only).
- Upstream protocols: **DoH, DoT, and DoQ** (`quic://host:853`, handled by dnsproxy via quic-go).
  DoQ caveat: needs **UDP/853**, often blocked on restrictive networks/firewalls → fall back to
  DoT/DoH, and surface per-upstream health (§9). DoQ hostnames: Quad9 `dns.quad9.net:853`,
  ControlD `quic://pN.freedns.controld.com` / `<id>.dns.controld.com`, NextDNS
  `quic://<id>.dns.nextdns.io:853`.
- Default upstream pool + per-domain upstreams via `proxy.UpstreamConfig.DomainReservedUpstreams`
  / `SpecifiedDomainUpstreams` / `SubdomainExclusions`.
- **Integration pitfalls (verified against current master):**
  - There is no `BeforeRequestHandler`. `Config.RequestHandler` **replaces** `DefaultHandler`; the
    handler must call `p.Resolve` exactly once (double-forwarding trap) or short-circuit.
  - Filtering runs **before** the cache (within `Resolve`) — correct order; follow AdGuard Home's
    `internal/dnsforward` pipeline as the reference.
  - **Flush the dnsproxy cache on every state change** (profile/upstream/rule/blocklist change,
    disable) or stale answers outlive edits.
  - Hand-built blocked / `$dnsrewrite` responses are **not cached** — set TTL/RA/flags yourself.
  - The `[/domain/]upstream` text parser lives in dnsproxy's `internal/cmd` (not importable) →
    we write our own parser for `#`, `//`, wildcards, and DS semantics (AdGuard Home's is the
    reference). Budget for this in phase 3.
- Cache: bounded size; optimistic mode deferred.

---

## 6. Filtering (urlfilter) — GPLv3

- Build `filterlist.RuleStorage` from configured lists (hosts + AdGuard/ABP syntax); lists are
  user-supplied (defaults disabled).
- `urlfilter.NewDNSEngine(storage)` → `MatchRequest` before forwarding.
- Blocked response: `0.0.0.0` / `::` (configurable) or NXDOMAIN. Support `$dnsrewrite`.
- List updates: **manual + on-start with ETag** (no scheduled updater in v1). Reload with a cache
  flush (§5).

---

## 7. Providers (presets)

Presets are **dual-stack** (IPv4 + IPv6) and each exposes: filtering variant, protocol
(DoH/DoT/**DoQ**), bootstrap mode, and profile/client id. Endpoints verified against provider docs
(2026-10); verify again at preset-release time. **DoQ ships in v1** (as an *upstream* protocol — see
§5).

### 7.1 Quad9 — 4 variants (all validate DNSSEC since 2026-06-15)
| Variant | Filtering | DoH | DoT/DoQ host | IPv4 | IPv6 |
|---|---|---|---|---|---|
| Secure (recommended) | malware block | `https://dns.quad9.net/dns-query` | `dns.quad9.net:853` | `9.9.9.9`, `149.112.112.112` | `2620:fe::fe`, `2620:fe::9` |
| No Threat Blocking | none | `https://dns10.quad9.net/dns-query` | `dns10.quad9.net:853` | `9.9.9.10`, `149.112.112.10` | `2620:fe::10`, `2620:fe::fe:10` |
| Secure + ECS | malware block + ECS | `https://dns11.quad9.net/dns-query` | `dns11.quad9.net:853` | `9.9.9.11`, `149.112.112.11` | `2620:fe::11`, `2620:fe::fe:11` |
| No Blocking + ECS | none + ECS | `https://dns12.quad9.net/dns-query` | `dns12.quad9.net:853` | `9.9.9.12`, `149.112.112.12` | `2620:fe::12`, `2620:fe::fe:12` |

- **Correction to earlier assumption:** `9.9.9.10` is *not* "unsecured/no-DNSSEC" anymore — Quad9
  enabled DNSSEC on all endpoints 2026-06-15; it means *No Threat Blocking* only. `9.9.9.11` =
  Secure+ECS, `9.9.9.12` = No-blocking+ECS.
- DoH-by-IP works (certs include service IPs) → bootstrap-free. ECS variants leak a network prefix:
  label clearly, never default to them. `dns9` is a legacy alias — skip.
- ⚠️ Confirm `2620:fe::9` (current docs) vs the 2018-blog `2620:fe::fe:9` at release.

### 7.2 NextDNS — Ultralow vs Anycast (a real architectural tradeoff)
`<id>` = profile id; optional device name (DoH path `/<id>/<device>`).

- **Ultralow (default, preferred):** `https://dns.nextdns.io/<id>`, DoT `<id>.dns.nextdns.io:853`.
  **Requires connecting by hostname with NO bootstrap IP** — supplying any bootstrap pins you to
  anycast (per NextDNS staff). Cost: one system-resolver lookup at startup (chicken-and-egg) to
  resolve the DoH hostname → gate behind a setting.
- **Anycast (bootstrap-pinned):** force via `dns1`/`dns2` (primary/secondary) hostnames; bootstrap
  v4 `45.90.28.0`/`45.90.30.0`, v6 `2a07:a8c0::`/`2a07:a8c1::`. Use when we must avoid the system
  resolver. (Legacy v6 embeds the id: `2a07:a8c0::<id-split>`.)
- **Forced edge server — NOT supported by the provider.** The guide you linked is a DNS-**rewrite**
  workaround (`ipv4-anycast.dns1.nextdns.io` → `ipv4-<server>-<N>.edge.nextdns.io`, servers from
  `ping.nextdns.io`); a NextDNS engineer calls it *"technically wrong,"* and staff say technical
  hostnames *"should not be used directly."* We will **not** ship it as a first-class toggle. Escape
  hatch: advanced users may set a custom upstream + a name-override rule (§ rules), unsupported.
- ⚠️ `dns1.nextdns.io/<id>` and `ultralow.*`/`anycast.*` hostnames are forum-reported, not
  provider-documented; only `https://dns.nextdns.io/<id>` is published.

### 7.3 ControlD — custom + free presets
- **Custom:** DoH `https://dns.controld.com/<id>` (DoH-by-IP works), DoT `<id>.dns.controld.com`;
  v4 `76.76.2.22`/`76.76.10.22`, v6 `2606:1a40::22`/`2606:1a40:1::22`. Client name: `/<id>/<name>`.
- **Free:** `https://freedns.controld.com/p0…p3` (p0 unfiltered, p1 malware, p2 ads/tracking, p3
  social), DoT `pN.freedns.controld.com`; v4 `76.76.2.11`/`76.76.10.11`, v6
  `2606:1a40::11`/`2606:1a40:1::11`.
- **Named free presets** (`x-oisd`, `x-stevenblack`, `x-hagezi-*`, `x-adguard`, `family`,
  `uncensored`, …) share the p0 bootstrap IPs — expose as presets. **Never ship a shared/preset id as
  the user's own.** ⚠️ custom DoQ hostname is inferred (`<id>.dns.controld.com`), not documented.

### 7.4 Dual-stack requirement (all presets)
- Resolver listens on `127.0.0.1:53` **and** `[::1]:53`.
- Every preset configured with **both** IPv4 and IPv6 endpoints and **both** bootstrap families.
- Health check runs **per family** (§9); IPv6-disabled host must not abort enable (skip v6, warn).
- `EnableEDNSClientSubnet` **off** by default (only the explicit `*ECS` Quad9 variants enable it).

### 7.5 Custom
User-entered DoH/DoT URL + optional bootstrap (v4/v6). Raw upstream escape hatch for power users.

---

## 8. Config schema (sketch)

```yaml
mode: on              # on | off
profile: default      # single active profile in v1
profiles:
  default:
    upstreams: ["https://dns.quad9.net/dns-query"]
    bootstrap: [9.9.9.9, 149.112.112.112]
    per_domain:
      - domains: ["corp.example"]
        upstream: "tls://10.0.0.1"
    cache: { enabled: true, size_bytes: 67108864 }
    filtering:
      enabled: true
      lists:
        - url: https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt
          enabled: true
      block_response: 0.0.0.0   # or nxdomain
```

State file (JSON, service-owned, ACL SYSTEM+Admins, atomic, schema-versioned): interception mode,
snapshot, dirty marker, active profile, last health result.
**Single-instance guard**: a lock so CLI and service never race the snapshot.

---

## 9. Lifecycle

- **Service start**: load config; if dirty marker → **restore from snapshot first** (never
  re-snapshot while dirty); if mode=on → re-enable. Retry/fallback if network not yet up (don't
  abort-or-restore loop).
- **Enable**: §4.3 strict order (bind → two-stage health check → snapshot+dirty → apply → flush).
- **Disable**: stop resolver → restore (delete NRPT rule / reset adapters) → **flush dns** → clear
  dirty marker.
- **Two-stage health check**: (a) query a random synthetic name **only our handler answers** (proves
  loopback→us, not "something on :53"); (b) query a random uncached name **per upstream family**
  (proves upstream, catches IPv6-dead-while-IPv4-ok).
- **Crash/kill**: dirty marker forces restore on next start. Add SCM failure actions (restart) plus
  a startup recovery task (belt-and-suspenders: dead service + loopbacked adapters = total outage).
- **Exit**: always restore; flush.

---

## 10. Build, packaging, signing

- Dev (Linux): `GOOS=windows GOARCH=amd64 go build ./...` for compile checks + pure-Go unit tests.
  Cannot test adapter/NRPT/service/tray locally → **Windows 11 VM/CI is a phase-1 prerequisite**.
- Tray uses `fyne.io/systray` → CGO. Gate the tray package behind a **build tag** so Linux
  `go build ./...` doesn't need MinGW. **Release builds run on `windows-latest`** (native Go).
- Signing: OV code-signing cert (EV only needed for a kernel driver, which we don't ship). GH
  Actions + `signtool`.
- Ship: service installer (`sc create` / WiX) + tray exe + config + LICENSE (GPLv3).

---

## 11. Phases / milestones

1. **Walking skeleton** — Go module; embedded dnsproxy on loopback forwarding DoH to ControlD;
   CLI `enable`/`disable`/`restore`. **Verify NRPT works on 24H2**; correct enable order; atomic
   snapshot + dirty recovery. *No UI.*
2. **Config + providers** — YAML config, presets, DoH/DoT selection, single-instance guard.
3. **Rules + filtering** — domain-rule parser (dnsmasq syntax) + urlfilter blocklists; cache flush.
4. **Service + tray** — service wrapper, SDDL-locked pipe, build-tagged tray, drift watcher +
   reconcile loop.
5. **Packaging + signing** — installer, GH Actions, docs.

### Cut from v1 (YAGNI — add on demand)
optimistic cache · multi-profile system · scheduled list updates · ARM64 · log-viewer UI ·
rule-editor UI (YAML only) · `$dnsrewrite` UX.

---

## 12. Verification (per phase)

- **1**: `dig @127.0.0.1` resolves; two-stage health check passes; after `enable`, `nslookup` goes
  through loopback and capture on 443 confirms DoH (no plaintext 53); `disable` restores (NRPT
  rule gone / adapters == snapshot); `kill -9` then restart triggers restore; **NRPT behavior on
  24H2 confirmed**.
- **2**: unit tests — config parse, preset expansion, single-instance guard.
- **3**: unit tests — per-domain rule selects correct upstream; blocked → `0.0.0.0`; allowlist
  overrides; cache flushed on rule change.
- **4**: SCM install/start/stop; tray reflects state; pipe rejects non-admin writes; drift
  re-applied; reconcile loop catches a simulated registry change.
- **5**: signed exe installs on clean Win11 VM (SmartScreen OK); end-to-end with real provider.

**Constraint:** phases 1 & 4 require a Windows 11 VM/CI; Linux runs compile checks + pure-Go unit
tests only. Set this up first.

---

## 13. Risks / open questions

- **Bypass/leak surface** (§1) — accepted; tray must surface it. WinDivert (LGPLv3) or WFP driver
  are the only ways to close it (phase 2 decision).
- **NRPT on 24H2** — verify in phase 1; per-adapter is the fallback.
- **`dnsproxy` API is version-sensitive** — pin a tag; verify `Config`/handler fields against
  source, not docs.
- **NextDNS Ultralow vs Anycast** (§7.2) — ultralow (better routing) needs a system-resolver
  bootstrap lookup; anycast is bootstrap-pinned but worse routing. Make it a user-visible choice.
- **NextDNS forced edge** (§7.2) — unsupported by the provider; document as an unsupported escape
  hatch, do not build first-class.
- **Quad9 ECS variants / captive portals** — ECS leaks a prefix (opt-in only); captive-portal
  back-off must ship (else user can't reach the login page).
- **GPLv3 obligations** — ship LICENSE + source; document in README.

---

## 14. Why not just wrap dnscrypt-proxy?
dnscrypt-proxy / AdGuard Home / NextDNS CLI share the same *architecture* (loopback resolver). We
build because the ask is **service + tray + per-domain rules + blocklists with one-click provider
switching and clean restore** — integration they don't ship. We reuse the hardened engine
(`dnsproxy`) rather than reimplement it.
