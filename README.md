# dns-client

A system-wide encrypted DNS client for Windows 11, in the spirit of YogaDNS.

Runs a local loopback resolver and points the OS at it, so DNS goes over
**DoH / DoT / DoQ** to a provider of your choice, with per-domain routing,
blocklists, and a tray UI. No kernel driver required.

See [SPEC.md](SPEC.md) for the full design: architecture, interception
mechanism, provider presets, phases, and verification plan.

## Status

Pre-implementation. Spec complete; starting phase 1 (walking skeleton).

## Providers

- **ControlD** — custom resolver + free `p0`–`p3` and named presets
- **Quad9** — Secure, No-Blocking, and ECS variants (IPv4 + IPv6)
- **NextDNS** — Ultralow and Anycast modes
- **Custom** — any DoH/DoT/DoQ endpoint

## Stack

Go · [AdguardTeam/dnsproxy](https://github.com/AdguardTeam/dnsproxy) ·
[AdguardTeam/urlfilter](https://github.com/AdguardTeam/urlfilter) · `miekg/dns`

## Build

Cross-compile from Linux (pure Go):

```sh
GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui" ./cmd/...
```

Signing is done on a Windows runner (`signtool`).

## License

GPLv3 — see [LICENSE](LICENSE).
