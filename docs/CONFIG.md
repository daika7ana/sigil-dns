# Configuration reference

Sigil DNS reads one YAML file. Load and validate happen on every start;
precedence is **command-line flags > config file > built-in defaults**.

| Platform | Default path |
|---|---|
| Windows | `%ProgramData%\SigilDNS\config.yaml` |
| Linux/dev | `$XDG_CONFIG_HOME/sigildns/config.yaml`, else `~/.config/sigildns/config.yaml` |

`sigildns config path` prints the resolved path. `sigildns config init`
writes a default config and never overwrites an existing file.

A running config file is optional: with no file, built-in defaults are used
(ControlD free p0, 64 MiB cache). An invalid file is a hard error for
`enable`/`run`.

## Top-level fields

| Field | Type | Default | Rules |
|---|---|---|---|
| `schema_version` | integer | `1` | Must be `1`; any other value is rejected. |
| `mode` | string | `off` | `on` or `off`. With `on`, the service re-enables interception on start. |
| `profile` | string | `default` | Must name an entry in `profiles`. |
| `profiles` | map | one `default` profile | Map of profile name → profile block. |
| `log` | block | `off` | Optional service file logging. See below. |

Precedence overrides: `--upstream`, `--bootstrap`, and `--profile` replace the
resolved profile's values at runtime.

## `profiles.<name>`

| Field | Type | Default | Rules |
|---|---|---|---|
| `upstreams` | list of strings | `["https://freedns.controld.com/p0"]` | Required, non-empty; each entry non-empty. |
| `bootstrap` | list of strings | `["76.76.2.11:53"]` | Each entry non-empty. May be empty for NextDNS ultralow. |
| `fallback_profile` | string | empty | Optional name of another profile; see below. |
| `cache` | block | `{enabled: true, size_bytes: 67108864}` | See below. |
| `per_domain` | list of rules | empty | See below. |
| `filtering` | block | disabled | See below. |
| `provider` | string | empty | Optional, display-only. Never used for resolution. |
| `variant` | string | empty | Optional, display-only. Never used for resolution. |
| `protocol` | string | empty | Optional, display-only. Never used for resolution. |

`provider`, `variant`, and `protocol` are free-form labels (for example
`NextDNS`, `ultralow`, `doh`). They are trimmed but otherwise unvalidated, are
never derived from the upstream URL, and do not affect resolution. They exist so
a GUI can show a friendly profile name without parsing secrets out of the
upstream.

### Fallback profiles

A profile may name another profile as its fallback:

```yaml
profiles:
  default:
    upstreams: ["https://dns.quad9.net/dns-query"]
    fallback_profile: backup
  backup:
    upstreams: ["https://dns.google/dns-query"]
    bootstrap: [8.8.8.8]
```

When every upstream of the active profile fails (timeout, unreachable, TLS/HTTP
failure), dnsproxy retries the query against the fallback profile's upstreams.
This is per query and automatic: the active profile does not change, and status
still shows the primary profile. Only the fallback's `upstreams` and `bootstrap`
are used; its `per_domain`, `filtering`, and `cache` blocks are ignored, while
the primary profile's per-domain rules and filtering still apply. Chains are not
followed — the fallback profile's own `fallback_profile` is ignored.

The name must exist in `profiles`; self-reference is rejected. Renaming a profile
updates references, and deleting one that is another profile's fallback is
refused until that reference is cleared. Empty or omitted means no fallback.

This covers the upstream provider being down or slow. If the Sigil DNS service
itself is not running, nothing listens on loopback and no fallback can help.

### `cache`

| Field | Type | Default | Rules |
|---|---|---|---|
| `enabled` | bool | `true` | Enables the dnsproxy cache. |
| `size_bytes` | integer | `67108864` (64 MiB) | Must be ≥ 0. If `enabled` is true, must be > 0. Missing value is normalized to 64 MiB. |

### `per_domain` (dnsmasq-style routing)

Each rule is one of two kinds. An **upstream** rule routes its domains to a
resolver:

```yaml
per_domain:
  - domains: ["corp.example"]
    upstream: "tls://10.0.0.1"
```

A **bypass** rule resolves its domains with the system's normal DNS:

```yaml
per_domain:
  - domains: ["printer.lan"]
    bypass: true
```

| Field | Type | Rules |
|---|---|---|
| `domains` | list of strings | Required, non-empty. A domain must not contain `/`. |
| `upstream` | string | Required non-empty on an upstream rule: any DoH/DoT/DoQ URL, or `#`. Forbidden on a bypass rule. |
| `bypass` | bool | Default `false`. Set `true` to resolve the domains via the system's normal DNS instead of Sigil DNS. Forbids `upstream`. |

Matching semantics (dnsproxy's dnsmasq parser):

- A domain matches **itself and all subdomains**; the **most specific** rule wins.
- `*.sub.example` matches subdomains only, **not** `sub.example` itself.
- `""` (empty string) matches **unqualified / single-label** names only.
- `#` as `upstream` **excludes** those domains from any reserved upstream, so
  they fall back to the default `upstreams` pool.
- A `bypass: true` rule sends its domains to the system's normal DNS — they are
  not routed through Sigil DNS's resolver and are not covered by its upstream,
  filtering, or cache. See [LIMITATIONS](LIMITATIONS.md).
- DS queries for a secure delegation (e.g. `secure.corp.example`) are resolved
  through the parent zone's upstream (`corp.example`).
- A trailing dot is accepted and normalized (`example.com.` → `example.com`).

### `filtering`

```yaml
filtering:
  enabled: true
  block_response: 0.0.0.0
  block_ttl: 3600
  lists:
    - url: https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt
      enabled: true
    - url: /path/to/local/hosts.txt
      enabled: true
```

| Field | Type | Default | Rules |
|---|---|---|---|
| `enabled` | bool | `false` | Filtering is off unless this is true **and** at least one list is enabled. |
| `lists` | list | empty | Each entry needs a non-empty `url`. |
| `lists[].url` | string | — | An HTTP(S) URL or a local file path. |
| `lists[].enabled` | bool | `false` | Disabled lists are ignored. |
| `block_response` | string | `0.0.0.0` | `0.0.0.0` or `nxdomain`. |
| `block_ttl` | integer (uint32) | `3600` | TTL for hand-built blocked answers. |

**`block_response` values:**

| Value | Effect on a blocked A/AAAA query |
|---|---|
| `0.0.0.0` | Answers `0.0.0.0` (A) / `::` (AAAA) with `block_ttl`. |
| `nxdomain` | Answers NXDOMAIN with no records. |

Lists are hosts-format and AdGuard/ABP syntax, loaded manually (no scheduled
updater in v1). Changing rules or lists flushes the resolver cache.

## `log`

Optional service-owned file logging (the GUI **Logging** page writes this). Off
by default, and applied at the **next service start** — changing the level or path
does not restart the service.

```yaml
log:
  level: off          # off | error | info | debug
  path: ""            # empty = %ProgramData%\SigilDNS\sigildns.log
  max_size_mb: 10
  keep_mb: 3
```

| Field | Type | Default | Rules |
|---|---|---|---|
| `level` | string | `off` | `off`, `error`, `info`, or `debug`. |
| `path` | string | empty | Empty = `%ProgramData%\SigilDNS\sigildns.log`. A custom path must be absolute and not a `\\.\` device path. |
| `max_size_mb` | integer | `10` | Rotate when the file reaches this size (clamped). |
| `keep_mb` | integer | `3` | After rotation keep the newest this many MB (clamped to `max_size_mb`). |

Resolver ids and URL credentials (userinfo, query strings, `sdns://`) are
**redacted at every level**. The `debug` level additionally records DNS query
names and upstream traffic, so treat a `debug` log as sensitive; the log itself
stays ACLed to SYSTEM + Administrators beside `config.yaml`.

## Worked examples

### Quad9 Secure (malware blocking, DNSSEC-validating)

```yaml
schema_version: 1
mode: on
profile: default
profiles:
  default:
    upstreams:
      - https://dns.quad9.net/dns-query
    bootstrap:
      - 9.9.9.9
      - 149.112.112.112
      - "2620:fe::fe"
      - "2620:fe::9"
    cache:
      enabled: true
      size_bytes: 67108864
```

Generate the exact block:

```powershell
sigildns providers show quad9/secure/doh
sigildns providers show quad9/secure/dot   # tls://dns.quad9.net:853
sigildns providers show quad9/secure/doq   # quic://dns.quad9.net:853
```

### NextDNS (Ultralow; no bootstrap on purpose)

```yaml
schema_version: 1
mode: on
profile: nextdns
profiles:
  nextdns:
    upstreams:
      - https://dns.nextdns.io/<your-profile-id>
    bootstrap: []          # ultralow: empty on purpose for best routing
    cache:
      enabled: true
      size_bytes: 67108864
```

Ultralow needs one system-resolver lookup at startup to resolve the DoH
hostname. To avoid the system resolver, use `anycast`, which pins anycast IPs
(worse routing):

```powershell
sigildns providers show nextdns/ultralow/doh --id <id>
sigildns providers show nextdns/anycast/doh  --id <id>
```

### ControlD

Custom profile:

```yaml
schema_version: 1
mode: on
profile: controld
profiles:
  controld:
    upstreams:
      - https://dns.controld.com/<your-id>
    bootstrap:
      - 76.76.2.22
      - 76.76.10.22
      - "2606:1a40::22"
      - "2606:1a40:1::22"
    cache: { enabled: true, size_bytes: 67108864 }
```

Free preset (no id): `p0` unfiltered, `p1` malware, `p2` ads/tracking, `p3`
social, plus named lists (`x-oisd`, `x-stevenblack`, `x-adguard`, `family`,
`uncensored`).

```powershell
sigildns providers show controld/p1/doh
sigildns providers show controld/custom/doh --id <id>
sigildns providers show controld/x-hagezi/doh --id x-hagezi-pro
```

### Per-domain routing + blocklist

```yaml
schema_version: 1
mode: on
profile: default
profiles:
  default:
    upstreams:
      - https://dns.quad9.net/dns-query
    bootstrap: [9.9.9.9, 149.112.112.112]
    cache: { enabled: true, size_bytes: 67108864 }
    per_domain:
      - domains: ["corp.example"]
        upstream: "tls://10.0.0.1"
      - domains: ["public.example"]
        upstream: "#"          # use the default pool for this one
      - domains: ["printer.lan", "*.lan"]
        bypass: true           # resolve via the system's normal DNS
    filtering:
      enabled: true
      block_response: nxdomain
      block_ttl: 3600
      lists:
        - url: https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt
          enabled: true
```
