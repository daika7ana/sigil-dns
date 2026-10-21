# Limitations and interception surface

Sigil DNS intercepts the **Windows DNS Client** path — the resolver that
normal Windows applications use. It does not, and without a kernel driver
cannot, intercept traffic that never asks the OS resolver. This page is the
honest version of that boundary.

## What is intercepted

- Applications that resolve names through the standard OS resolver
  (`getaddrinfo` / `DnsQuery`). This is the overwhelming majority of software:
  browsers (with Secure DNS off), mail clients, updaters, CLIs.
- Queries on every adapter, present and future, because interception is a single
  NRPT rule with namespace `.` (`Add-DnsClientNrptRule -Namespace '.' -NameServers
  127.0.0.1,::1`), not per-adapter settings.
- Both address families: the resolver listens on `127.0.0.1:53` and `[::1]:53`.

## What is not intercepted

| Scenario | Why it bypasses |
|---|---|
| **Apps with hardcoded resolver IPs or their own stub resolver** | They send DNS straight to `8.8.8.8` or a DoH endpoint themselves and never consult the OS resolver. |
| **Browsers with built-in Secure DNS (DoH)** | The browser resolves names itself over HTTPS to its own chosen provider. You must disable Secure DNS in each browser. |
| **`hosts` file / LLMNR / mDNS / NetBIOS** | These resolve locally without DNS at all. Sigil DNS never sees them. |
| **DNS-level blocklists** | The blocklists only see traffic that reaches the resolver, so they inherit every gap above. |

## DNSSEC

DNSSEC validation is delegated to the upstream resolver. With the cache enabled
(the default), queries carry the DO bit, so validating presets (Quad9,
Cloudflare, NextDNS, ControlD) return authenticated answers and signatures are
cached.

Sigil DNS does not validate DNSSEC itself, and cannot without a validating
recursor: block, hosts and rewrite answers are synthesized locally and can never
carry a valid signature. Filtered names are therefore intentionally unsigned.
A plain UDP/53 custom upstream can also have the upstream's AD bit stripped by
an on-path attacker; prefer DoH/DoT/DoQ if that matters.

## What silently breaks or leaks

### New or transient adapters — leak window

Interception is applied to the machine, not to individual adapters, so a newly
connected adapter is covered as soon as Windows consults NRPT. However, if an
adapter is added, reverted (e.g. by a driver reset), or missed while the service
is restarting, parallel multi-homed queries can briefly leak to a non-loopback
resolver. A registry watcher on the Tcpip/Tcpip6 interface keys plus a 45 s
reconcile loop re-apply interception and detect this drift, but the window
between the change and the reconcile is real.

### VPN that installs NRPT rules

A VPN can install an equal-or-more-specific NRPT rule (for example a catch-all
`{.}` rule). Windows gives the more specific rule precedence, so:

- our rule may be **overridden** for the VPN's namespaces, and
- the VPN's own DNS may **hard-break** because it expects to own resolution.

Sigil DNS detects conflicting foreign NRPT rules and **warns instead of
fighting them**. It does not rewrite the VPN's rules. If VPN DNS stops working,
remove the VPN or stop Sigil DNS.

### Per-domain bypass rules

A `per_domain` rule with `bypass: true` deliberately sends its domains to the
system's normal DNS instead of the resolver. On Windows this is a more-specific
NRPT rule with no name servers (an exemption), so the DNS client uses its normal
interface DNS for those names.

- **They leave the encrypted path.** Bypassed names are not covered by the
  encrypted upstream, the blocklists/filtering, or the cache; they go out however
  the machine's normal DNS is configured (often plaintext UDP/TCP 53).
- **The exemption is verified and has a fallback.** The empty-name-server NRPT
  encoding is not first-class in the Windows DnsClient cmdlets, so Sigil DNS
  verifies the exemption after enabling. If it did not take effect, it pins the
  interface's current non-loopback DNS servers for that namespace instead; if
  even that cannot be verified it logs a warning and the domain keeps being
  resolved by Sigil DNS normally. An enable never fails because one bypass
  could not be applied.
- **Multi-homed fallback.** The fallback uses the union of non-loopback DNS
  servers across interfaces, which is not necessarily Windows' normal
  per-interface choice, and a DHCP renewal can overwrite it.
- **A domain GPO overrides local rules.** A machine-wide domain GPO NRPT policy
  takes precedence over all local NRPT rules, so do not use bypass where such a
  policy exists.

### Captive portals (pre-auth)

Before you authenticate to a Wi-Fi captive portal, the portal works by
intercepting plain DNS and HTTP. With encrypted upstreams and NRPT interception
active, the bootstrap lookup for the DoH endpoint fails and you may be unable to
reach the login page. v1 backs off on failure so you can reach the portal, but
this is a known rough edge: **disable Sigil DNS first on a network you have
not yet authenticated to.**

### Persistent drift

If Windows, Group Policy, MDM/Intune, or another DNS tool keeps rewriting the
interception out from under the service, the reconcile loop retries. After
`DefaultMaxAttempts` (3) consecutive failed re-applies it **auto-disables and
logs an error** rather than looping forever. The service stays up in a degraded
state so you can `status`/`disable` it.

## Port 53 and ICS

The resolver must bind UDP **and** TCP on both `127.0.0.1:53` and `[::1]:53`.
Common owners of port 53 are:

- `SharedAccess` / Internet Connection Sharing (ICS), often started by Hyper-V,
  WSL, Docker, or the Host Network Service (HNS);
- a Docker backend or another local resolver.

If the bind fails, `enable` aborts and reports the owner rather than silently
failing. Stopping ICS usually frees the port, but that also tears down NAT for
Hyper-V/WSL/Docker networking.

Do **not** disable the Windows DNS Client service to free the port — it is
mandatory on Windows 11 24H2.

## Endpoint security / IPS / DPI

Network inspection software (host firewalls, IPS/DPI, some EDR suites) may:

- block outbound **UDP/TCP 853** (DoT/DoQ) or **443** (DoH),
- flag or throttle encrypted DNS, or
- reset the connection.

DoQ needs UDP/853 and is the most likely to be blocked on restrictive networks;
fall back to DoT or DoH if an upstream reports unhealthy. If every upstream
fails, the two-stage health check fails and `enable` aborts, leaving DNS
untouched.

## WSL

WSL2 on the default NAT network does not always route through the Windows
loopback, so WSL workloads can bypass Sigil DNS. Options:

- **`dnsTunneling`** (`.wslconfig`): WSL can tunnel DNS to the Windows host
  resolver, in which case WSL DNS follows Windows and is intercepted.

  ```ini
  # %UserProfile%\.wslconfig
  [wsl2]
  dnsTunneling=true
  ```

- Mirrored networking mode (`networkingMode=mirrored`) generally makes WSL use
  the host's resolver directly.

Neither is automated in v1. If WSL DNS does not behave as expected, verify with
`resolv.conf` / `wsl.exe --debug-shell` which resolver WSL is actually using.

## Why no kernel driver

Closing every gap above requires WFP/NDIS-level interception. That was
explicitly out of scope for v1 (see SPEC §1 Non-goals). The trade is a
driver-free, easy-to-restore tool with a documented bypass surface, versus a
kernel driver with much wider reach and much higher install/rollback risk.
