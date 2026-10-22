# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed
- Documentation: cleaned up SPEC.md and README.md, added badges, and started this changelog.
- Release notes now use the matching `CHANGELOG.md` section, with `release-notes/<tag>.md` as an optional override.

## [0.2.0] - 2026-10-22

### Added
- Opt-in system tray icon with live service status and Open, Open log file, Clear DNS cache and Exit actions.
- Minimize-to-tray and close-to-tray options.
- Start tray icon at sign-in via a per-user ONLOGON scheduled task.

### Changed
- Removed the dormant per-adapter fallback and deduplicated shared code paths.

### Fixed
- Restored input focus when the window opens from the tray.
- Warn when shutdown restore fails.
- Reclaim a state lock whose pid can no longer be read.

## [0.1.3] - 2026-10-21

### Changed
- Migrated to Go 1.27.3 and adopted goroutine leak checks.

## [0.1.2] - 2026-10-21

### Fixed
- Never run the service with interception off: startup always enables, and the SCM state stays StartPending until recovery and enable succeed.
- Updated tests for startup enable and config-error status.

## [0.1.1] - 2026-10-21

### Changed
- Consolidated installer scripts into a single `setup.ps1` with an install/uninstall menu and UAC relaunch.
- CI fails closed on red runs, verifies release packages and checksums, skips duplicated checks on tag builds, cancels superseded PR runs and serializes releases.

## [0.1.0] - 2026-10-21

### Added
- Go module and walking-skeleton resolver (embedded dnsproxy on `127.0.0.1:53` / `[::1]:53`) with CLI `enable`/`disable`/`restore`.
- YAML config (`schema_version` 1) with provider presets: Quad9, Cloudflare, NextDNS, ControlD and custom; DoH, DoT and DoQ.
- Per-domain routing rules (dnsmasq semantics) and urlfilter blocklists (hosts/AdGuard/ABP, answered with `0.0.0.0`/`::` or NXDOMAIN).
- Windows service host (LocalSystem) with SDDL-locked control pipe and sanitized status pipe.
- Drift watcher with a 45 s reconcile loop, two-stage health check, atomic state and dirty-marker crash recovery.
- Config and state ACLs (SYSTEM + Administrators) and a redacting log handler.
- Opt-in service file logging with rotation.
- go-gui GUI with Status, Settings, Rules, Logging and About pages; elevated `--apply` helper with DPAPI staging; full profile/rules/list editing and service install/start/stop.
- `installer/setup.ps1`, Start Menu shortcut, release workflow with optional signtool signing, draft-first publishing and `SHA256SUMS`.
- Fallback profiles and the DNSSEC DO bit on cached queries.

### Changed
- Renamed the project from dns-client to Sigil DNS.

### Fixed
- Audit-driven fixes: fail-closed uninstall (restore, stop, verify, delete) and `--apply` containment.

### Security
- Resolver ids kept out of IPC, logs and argv (admin-only config/state files); status DTOs sanitized; ACL failures are hard errors.
