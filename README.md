# dns-client

System-wide encrypted DNS for Windows 11, without a kernel driver.

`dns-client` runs a loopback DNS resolver on `127.0.0.1:53` and `[::1]:53` and
points Windows at it with a single NRPT (Name Resolution Policy Table) rule
covering all namespaces. Applications that use the normal OS resolver then send
their DNS over **DoH / DoT / DoQ** to a provider you choose, with per-domain
routing and local blocklists, and `disable` cleanly restores your original
settings.

The design goal is the *outcome*: one rule in, one restore out, no per-adapter
churn, no driver. See [SPEC.md](SPEC.md) for the full design.

## Features

- **Providers** — Quad9, Cloudflare, NextDNS, ControlD, or any custom DoH/DoT/DoQ URL.
- **Encrypted upstreams** — DoH, DoT, and DoQ (`quic://host:853`).
- **Per-domain routing** — dnsmasq-style rules: send `corp.example` to an
  internal resolver while everything else uses your provider.
- **Blocklists** — hosts-format and AdGuard/ABP lists via
  `AdguardTeam/urlfilter`; blocked answers are `0.0.0.0`/`::` or NXDOMAIN.
- **Clean enable / disable / restore** — NRPT is added on enable and removed on
  disable; `restore` recovers after a crash.
- **Drift auto-reconcile** — a registry watcher plus a 45 s reconcile loop
  re-apply interception if Windows, GPO, or another tool rewrites it.
- **Crash recovery** — a dirty marker forces a restore on the next start, so a
  killed service can never leave the machine pointing at a dead resolver.
- **Service + CLI** — a single binary runs as the CLI or the Windows service.
- **GUI config client** — a full-parity Status + Settings window: one-click
  Enable / Disable / Restore, service install / remove, and profile / filtering
  / cache editing. The shipped binary requests admin via an embedded manifest, so
  it prompts UAC once at **launch** and runs elevated until you close it.

## Requirements

- Windows 11 x64.
- Administrator rights to install the service and to enable/disable
  interception (both touch NRPT and the SCM).

## Install

**Packaged installer** (preferred):

```powershell
# From an elevated PowerShell
.\installer\install.ps1
```

The installer also places a **Start Menu shortcut** named `dns-client` that
launches the GUI; run it from there any time after install.

**Manual** (from an elevated PowerShell, after building or downloading the
binary):

```powershell
dns-client config init            # writes a default config if none exists
dns-client service install        # register the service (runs as LocalSystem)
dns-client service start
```

`service install` registers the service to start automatically and points it at
the current executable with the `--service` argument. Uninstall with
`dns-client service uninstall` (which stops the service first and restores
interception).

> The service runs as **LocalSystem** in v1. The least-privilege virtual
> account can not create the IPC pipe instances, is locked out of its own state
> file, and lacks the rights NRPT/adapter writes need. See SPEC §2.

## Quickstart

```powershell
# 1. Create a config (never clobbers an existing file)
dns-client config init

# 2. Pick a provider and print its generated upstream block
dns-client providers list
dns-client providers show quad9/secure/doh
dns-client providers show nextdns/ultralow/doh --id <profile-id>
dns-client providers show controld/p1/doh

#    Paste the printed upstreams/bootstrap into your config, then set mode: on.
#    (Default config points at ControlD free p0 and is mode: off.)

# 3. Install and start the service (elevated)
dns-client service install
dns-client service start

# 4. Verify (the binary always requests admin, so this elevates too)
dns-client status
```

With `mode: on`, starting the service enables interception (bind → two-stage
health check → snapshot → NRPT → flush). Read status as JSON to confirm
`"mode": "on"` and `"dirty": false`.

To try the resolver without touching the system:

```powershell
dns-client run --upstream https://dns.quad9.net/dns-query
```

## GUI

`dns-client-gui.exe` is the full configuration client. It has two pages:
**Status** — a banner, a single power control (Enable / Disable, plus Restore for
a crash-leftover state), and profile / filtering / cache cards; and **Settings**
— service install / remove and state, a profile list with add / rename / delete
/ set-active, and a per-profile editor (provider / variant / protocol, a masked
resolver id, custom URL, bootstrap, filtering + block response, cache and size).
Enable starts the service and applies interception; Disable removes it. The
binary requests admin via an embedded manifest, so Windows shows **one UAC prompt
when you launch it** and the window runs elevated until you close it (this
applies to every `dns-client.exe` invocation, not just the GUI). A `gui.lock`
next to `gui.json` keeps a single instance. Launch it from the Start Menu
shortcut, or run `dns-client-gui.exe` with no arguments (the GUI binary opens the
window; `--gui` opens it explicitly). See [docs/GUI.md](docs/GUI.md).

## CLI reference

```
dns-client <command> [flags]
```

| Command | Description |
|---|---|
| `enable` | Bind listeners, health-check, snapshot, apply interception, then run until Ctrl-C. On exit, disable and restore. |
| `disable` | Stop the resolver, restore interception, clear the dirty marker. |
| `restore` | Restore interception from persisted state (crash recovery). Works even if the config file is corrupt. |
| `status` | Print the persisted state as JSON, or `state: none (<path>)` if never enabled. |
| `run` | Foreground resolver only, without touching interception (dev/CI). |
| `providers list` | List the provider preset catalog. |
| `providers show <provider>[/<variant>[/<protocol>]]` | Print the generated upstreams/bootstrap for a selection. |
| `config path` | Print the resolved config file path. |
| `config init` | Write a default config (never overwrites an existing file). |
| `service install` | Register the Windows service (start type automatic, LocalSystem). |
| `service uninstall` | Stop and remove the Windows service. |
| `service start` | Start the Windows service. |
| `service stop` | Stop the Windows service. |
| `--service` | Run the service host (invoked by the SCM, not by hand). |
| `--gui` | Run the GUI. Requires a `-tags gui` build; the GUI binary also opens it with no arguments. |
| `--tray` | Run the tray UI. Requires a `-tags tray` build. Deferred to v1.1. |
| `help`, `-h`, `--help` | Print usage. |

### Flags

Flags are accepted by `enable`, `disable`, `restore`, `status`, `run`, and
`config` (both `-flag` and `--flag` forms work, per Go's `flag` package):

| Flag | Description | Default |
|---|---|---|
| `--upstream <url>` | Upstream DNS URL. Highest precedence. | ControlD free p0 |
| `--bootstrap <addr,...>` | Bootstrap resolver address(es), comma-separated. | `76.76.2.11:53` |
| `--config <path>` | Config file path. | platform default (below) |
| `--profile <name>` | Config profile to use. | the config's active profile |
| `--listen <addr,...>` | Loopback listen addresses, comma-separated. | `127.0.0.1:53,[::1]:53` |
| `--state <path>` | State file path. | platform default (below) |

>`providers show` uses its own parser and accepts `--id`, `--upstream`, and
>`--bootstrap` (before or after the positional selection). Protocol is one of
>`doh`, `dot`, `doq`; default `doh`.

## Providers

Run `dns-client providers list` for the live catalog.

| Provider | Variants | Notes |
|---|---|---|
| `quad9` | `secure` (default), `noblock`, `secure-ecs`, `noblock-ecs` | `*-ecs` variants send EDNS Client Subnet and leak a network prefix. |
| `cloudflare` | `standard` (default), `security`, `family` | 1.1.1.1 / 1.1.1.2 (malware) / 1.1.1.3 (malware + adult). No id. `security`/`family` configure IPv4 bootstrap only. |
| `nextdns` | `ultralow` (default), `anycast` | Requires `--id`. `ultralow` uses no bootstrap (best routing); `anycast` pins anycast IPs. |
| `controld` | `custom` (default), `p0`–`p3`, `x-oisd`, `x-stevenblack`, `x-adguard`, `family`, `uncensored`, `x-hagezi`, `free` | `custom`, `x-hagezi`, and `free` require `--id`. Free presets share bootstrap IPs. |
| `custom` | `custom` | Raw user-entered `--upstream` URL + optional `--bootstrap`. |

All presets support DoH, DoT, and DoQ. Quad9, NextDNS, and ControlD configure
both IPv4 and IPv6; Cloudflare `security`/`family` configure IPv4 bootstrap only.

## Configuration

Configuration is a single YAML file.

| Platform | Config path |
|---|---|
| Windows | `%ProgramData%\dns-client\config.yaml` |
| Linux/dev | `$XDG_CONFIG_HOME/dns-client/config.yaml`, else `~/.config/dns-client/config.yaml` |

Precedence when starting: **command-line flags > config file > built-in
defaults**.

`config init` writes a minimal default: `mode: off`, one profile named
`default` using ControlD free p0 with a 64 MiB cache. A fuller annotated example
lives in [config.example.yaml](config.example.yaml); see
[docs/CONFIG.md](docs/CONFIG.md) for every field, validation rule, the
`block_response` values, and `per_domain` semantics.

State is stored separately, owned by the service and ACLed to SYSTEM +
Administrators:

| Platform | State path |
|---|---|
| Windows | `%ProgramData%\dns-client\state.json` |
| Linux/dev | `$XDG_STATE_HOME/dns-client/state.json`, else `~/.local/state/dns-client/state.json` |

## Known limitations

The table below is the honest interception surface. Anything not using the OS
resolver bypasses `dns-client` entirely.

| Scenario | Intercepted? |
|---|---|
| Normal apps using the OS resolver | Yes |
| Apps with hardcoded resolver IPs or their own stub resolver | No |
| Browsers with built-in Secure DNS (DoH) | No — must be disabled in-browser |
| Any adapter not currently loopbacked (new/transient/missed) | Leak window — parallel multi-homed queries can bypass |
| VPN that installs NRPT rules (e.g. `{.}`) | VPN rules may override or break ours; VPN DNS may hard-break |
| Captive portal (pre-auth) | Bootstrap DoH fails; the portal may be unreachable until we back off |
| `hosts` file / LLMNR / mDNS / NetBIOS local resolution | Bypasses DNS entirely |
| DNS-level blocklists | Only see OS-resolver traffic |

See [docs/LIMITATIONS.md](docs/LIMITATIONS.md) for the details, WSL notes, and
the IPS/port-53 caveat.

## Build from source

The repo ships a Docker wrapper so no host Go toolchain is needed:

```sh
dev/go build ./...          # compile check (Linux)
dev/go test ./...           # unit tests
GOOS=windows GOARCH=amd64 dev/go build -o dns-client.exe ./cmd/dns-client

# GUI binary (same module, GUI subsystem)
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 ./dev/go build -tags gui -ldflags "-H windowsgui" -o dns-client-gui.exe ./cmd/dns-client
```

Cross-compiling to Windows is pure Go. Release builds produce **two binaries**
from the one module: `dns-client.exe` (console subsystem — CLI, service host,
and the `--apply` helper) and `dns-client-gui.exe` (GUI subsystem, `-tags gui
-ldflags "-H windowsgui"` — the full Status + Settings client, still
`--apply`-capable). A single
exe cannot cleanly be both: a console-subsystem GUI leaves a stray console
window, and a `windowsgui` CLI loses stdout (breaking `dns-client status`). The
plain console build stays `dns-client.exe` with no gui tag.

CI runs on `windows-latest` (`.github/workflows/ci.yml`); tagged releases build
+ optionally sign on `windows-latest` (`.github/workflows/release.yml`). The tray
UI remains **deferred to v1.1** — the `--tray` hook exists but no tray UI is
built. CI additionally runs a `-tags tray` build to keep the tag compiling.

```sh
dev/go build -tags tray ./...
```

## Architecture note

The service runs elevated and owns the resolver, config, interception, and
state. The CLI binary itself also runs elevated (admin manifest), and talks to
the service over two SDDL-locked named pipes:

| Pipe | Purpose | Access |
|---|---|---|
| `\\.\pipe\dns-client` | State-changing operations | SYSTEM + Administrators (`D:P(A;;GA;;;SY)(A;;GA;;;BA)`) |
| `\\.\pipe\dns-client-status` | Read-only status | SYSTEM + Authenticated Users, read+write (`D:P(A;;GA;;;SY)(A;;GRGW;;;AU)`) |

The status payload is a sanitized DTO that omits the active upstream id and
interface GUIDs. There is no local HTTP control channel.

## License

GPLv3 — see [LICENSE](LICENSE). The project is GPLv3 because it statically links
`AdguardTeam/urlfilter`, which is GPL-3.0; the shipped binary is a combined
work. There is no way to avoid the copyleft by running urlfilter out-of-process.
