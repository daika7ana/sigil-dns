# AGENTS.md

Windows 11 system-wide encrypted DNS client (YogaDNS-class, no kernel driver).
Go module `github.com/daika7ana/dns-client`.

`SPEC.md` is the **authoritative design** — read §15 (GUI/privilege), §4.7 (IPC security),
§10 (packaging) before changing behaviour. `README.md` + `docs/{CONFIG,LIMITATIONS,TROUBLESHOOTING,RELEASE}.md`
are user-facing. The project is **GPLv3** because it statically links `AdguardTeam/urlfilter` (GPL-3.0).

## Commands (there is no Go toolchain on the host)
Use the Docker wrapper `dev/go` — never call `go` directly, never install a toolchain.

```sh
./dev/go build ./...                            # default build (GUI-free)
./dev/go build -tags gui ./...                  # include the GUI
GOOS=windows GOARCH=amd64 ./dev/go build ./...  # real cross-compile (pure Go)
./dev/go generate ./cmd/dns-client               # regenerate the admin/UAC manifest resource
./dev/go vet ./...
GOOS=windows GOARCH=amd64 ./dev/go vet ./...    # also compiles Windows-only _test.go files
./dev/go test ./...
./dev/go test -run TestName ./internal/ipc/     # single package / single test
./dev/go test -tags gui ./internal/gui/         # GUI headless smoke test
```

`dev/go` runs `golang:1.26` with the repo mounted at `/w` and forwards `GOOS`/`GOARCH`/`CGO_ENABLED`.
Host is Linux; the product target is **Windows x64 only** (pure Go — no cgo, no MinGW, no Node/WebView2).
The Windows binary embeds `cmd/dns-client/dns-client.manifest` (`requireAdministrator`), so it requests
admin/UAC on every launch.

## Environment gotchas
- `./dev/go build ./...` occasionally fails on a cold container/module cache. **Re-run** — a single FAIL is not proof.
- `pwsh` is not installed on the host, so `installer/*.ps1` and `scripts/*.ps1` cannot be syntax-checked locally; they are only parsed for real on CI.
- Windows-only code/tests never execute locally; `GOOS=windows ... vet` is the strongest local check.
- The GUI software renderer has no fonts in the container, so headless text assertions are shape/colour-based.
- `gh run list` must be filtered with **`--workflow CI`**: the auto dependency-graph workflow shares `headSha` and otherwise matches.
- `integration-windows` depends on real Quad9 DoH and is **occasionally flaky** (transient `upstream unreachable`); `gh run rerun <id> --failed` once before believing it.

## Architecture (the non-obvious parts)
One binary, entrypoint `cmd/dns-client/main.go`, many modes: CLI commands, `--service` (SCM host),
`--apply <path>` (elevated headless helper), `--gui`, `--tray`.

`internal/`: `lifecycle` (enable/disable/restore orchestration) · `state` (atomic JSON + dirty marker +
single-instance lock) · `intercept` (NRPT via PowerShell, primary; per-adapter fallback) · `resolver`
(embedded dnsproxy + filter + per-domain rules) · `filter` (urlfilter wrapper) · `providers` (presets) ·
`config` (YAML) · `ipc` (dual named pipes) · `service` (SCM host) · `drift` (registry watch + reconcile) ·
`elevate` (one-shot UAC helper + allowlisted config patch) · `gui` (`gui` tag, go-gui **config client**,
full-parity Status + Settings) · plus `logging`/`winacl`/`redact`/`version`/`health`.

- **Import cycle:** `internal/config` imports `internal/resolver`, so `internal/resolver` must NOT import
  `internal/config` (use the local `resolver.Rule` type).
- **Platform split** is `_windows.go` / `_other.go` build-tagged pairs. Every Windows implementation has a
  non-Windows stub returning a typed error so Linux builds stay green — add both when adding a feature.
- **Build tags:** `gui` = the go-gui window; `tray` = retired stub (leave it). Keep default/Linux builds
  GUI-free by keeping the tag.
- **GUI is a full config client**, not a status viewer (`internal/gui/`): `settings_view.go` (Settings
  page: service card + profile list add/rename/delete/set-active + per-profile editor), `draft.go`
  (editor buffer), `elevate_adapter.go` (**the only `internal/elevate` importer**; maps GUI actions to
  `elevate.Request`), `instance.go` (`gui.lock` single-instance guard), `render_test.go` (headless
  shape/colour assertions — no fonts in the container), `console_{windows,other}.go` (`detachConsole`).
  The GUI is the **full-permission surface**; the default/Linux build stays GUI-free behind the `gui` tag.
- **Elevated-helper allowlist** (`internal/elevate`): `Patch` is the whole config surface, not just
  `mode` — `Mode` / `ActiveProfile` / per-profile `Profiles` (provider/variant/protocol/id/url/bootstrap/
  filtering/cache) plus structural `AddProfile` / `RenameProfile` / `DeleteProfile`; actions also include
  `service_install|service_uninstall|service_start|service_stop`. Every field is validated (bootstrap as
  IP literals, provider/variant/protocol against the registry, id charset/length, explicit-zero
  rejection); ids travel only in the DPAPI-sealed body; blank id = keep. **No new IPC methods** — the
  §15.3 privilege table is unchanged.
- **Uninstall is ordered and fail-closed** (`uninstallPlan`/`uninstallSequence`): restore interception →
  stop the service → verify interception is actually down (NRPT probe + one local config-independent
  restore retry) → only then delete. It never deletes while interception is unproven down.
- **Config path hardening** (`internal/config/system_windows.go`): `SystemPath`/`SystemDir`/
  `SystemStatePath` resolve `%ProgramData%` via the KnownFolder API, never the env var, with no
  user-dir fallback; the service, the helper and the CLI all use these.
- **`providers.ExtractID`** recovers a resolver id from generated upstreams (provider + protocol aware;
  `ErrNoID`/`ErrAmbiguousID`); the helper uses it for replace-only id recovery and `id_set` detection.
- **IPC `ProfileSummary`** (`internal/ipc/summary.go`): sanitized per-profile view (name / active /
  labels / `id_set` / filtering+cache flags+sizes / counts / bootstrap literals) — never ids or URLs.
  `id_set` is provider-aware (`providers.ExtractID`), fixing the old DoH-path-only false negative for
  DoT/DoQ.
- **Logging** (`internal/logging` + `redact.Handler`): opt-in service-owned file log from the top-level
  `log:` config block (default `off`; `level` off|error|info|debug; `path` defaults beside `config.yaml`;
  10 MB cap, newest 3 MB kept). `cmd/dns-client` `serviceLogger()` is the single choke point; the
  redacting handler (seeded via `providers.ExtractIDs`, **re-seeded on every reload**) scrubs resolver
  ids (DoH-path and DoT/DoQ hostname label), URL userinfo/query and `sdns://` at all levels, while
  `debug` exposes query names. The GUI **tails the file directly** — no IPC method. The file is opened
  **without `O_APPEND`** so the handle keeps `FILE_WRITE_DATA`; rotation truncates through that open
  handle (`file.Truncate(0)` + seek/write). `os.Truncate` **by path** is only the GUI's "Clear log file".

## Invariants — do not break
- **Enable order:** bind listeners → two-stage health check → persist snapshot + dirty marker → apply
  interception → flush DNS. Never touch DNS settings before the resolver is proven listening.
- **Never re-snapshot while dirty.** Restore from the persisted snapshot first; re-snapshotting a
  loopbacked system captures `127.0.0.1` as the "original" and loses the user's real DNS.
- **`restore` must work with a corrupt/missing config** (config-independent) — recovery can never depend on YAML.
- **The service runs as `LocalSystem`** (deliberate: a virtual account cannot create the pipe instances, is
  locked out of its own state/config, and lacks NRPT/admin rights). Do not "restore" it to `NT SERVICE\...`.
- **Config and state are ACLed to SYSTEM + Administrators** (`internal/winacl`, applied by `config.Save`);
  ACL failure is a hard error, not best-effort.
- `disable`/`restore`/`status` deliberately use a **config-independent** lifecycle.

## Security rules (SPEC §15.3)
- Resolver ids live only in the admin-only config. **Never** put them on argv/env or send them over IPC;
  the redacting `redact.Handler` keeps them out of logs at every level. `debug` is the deliberate
  exception for query names (not credentials). The status pipe carries only sanitized DTOs.
- Control pipe `\\.\pipe\dns-client` = SYSTEM + Administrators only. Status pipe
  `\\.\pipe\dns-client-status` = read-only, sanitized. Never grant `AU` access to the control pipe.
- Elevation is explicit (`elevate.Apply` → `ShellExecuteExW runas` → `--apply`). The shipped exe also
  embeds a `requireAdministrator` manifest (see above), so it is elevated for its lifetime; the
  `--apply` helper stays one-shot per write and re-validates everything, applying only the allowlisted
  config surface (see Architecture). No persistent broker, no silent/auto elevation.

## CI & releases
- `.github/workflows/ci.yml`: ubuntu+windows matrix (`go build`, `-tags tray`, `-tags gui`, `vet`, `test`;
  GUI smoke test on both runners) plus `integration-windows` (real NRPT via `scripts/ci-windows-integration.ps1`).
- `.github/workflows/release.yml`: tag `v*` or `workflow_dispatch`; builds windows/amd64; signs with
  `signtool` only when `WINDOWS_CERT_PFX_BASE64` + `WINDOWS_CERT_PASSWORD` secrets exist (else skip +
  `::warning::`); zips and creates the GitHub Release. **The GUI exists only with `-tags gui`**, so any
  packaging step must pass it to ship a GUI binary.
- Always use `go-version-file: go.mod` in workflows — never `go-version: '1.26.x'` (setup-go v7 sets
  `GOTOOLCHAIN=local` and will not auto-fetch the toolchain `go.mod` requires).
- After pushing, confirm CI with `gh run watch <id> --exit-status` before calling it green. Commit style:
  imperative subject, no trailers.

## Pinned dependencies (why)
`AdguardTeam/dnsproxy v0.86.0` (engine; needs Go ≥1.26.8; **no `Config.BootstrapDNS` field** — bootstrap is
`upstream.Options.Bootstrap`) · `AdguardTeam/urlfilter v0.23.4` (**GPL-3.0**) · `go-gui-org/go-gui v0.86.0`
(GUI; pre-1.0, pin deliberately) · `Microsoft/go-winio v0.6.2` (**deliberately pinned** — v0.6.3 drags a
`golang.org/x/*` upgrade cascade). Verify library APIs against the pinned tag's source, not against docs.
