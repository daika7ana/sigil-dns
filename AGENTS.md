# AGENTS.md

Windows 11 system-wide encrypted DNS client (YogaDNS-class, no kernel driver).
Go module `github.com/daika7ana/sigil-dns`.

`SPEC.md` is the **authoritative design** — read §15 (GUI/privilege), §4.7 (IPC security),
§10 (packaging) before changing behaviour. `README.md` + `docs/{CONFIG,LIMITATIONS,TROUBLESHOOTING,RELEASE}.md`
are user-facing. The project is **GPLv3** because it statically links `AdguardTeam/urlfilter` (GPL-3.0).

## Commands (there is no Go toolchain on the host)
Use the Docker wrapper `dev/go` — never call `go` directly, never install a toolchain.

```sh
./dev/go build ./...                            # default build (GUI-free)
./dev/go build -tags gui ./...                  # include the GUI
GOOS=windows GOARCH=amd64 ./dev/go build ./...  # real cross-compile (pure Go)
./dev/go generate ./cmd/sigildns               # regenerate the admin/UAC manifest resource
./dev/go vet ./...
GOOS=windows GOARCH=amd64 ./dev/go vet ./...    # also compiles Windows-only _test.go files
./dev/go test ./...
./dev/go test -run TestName ./internal/ipc/     # single package / single test
./dev/go test -tags gui ./internal/gui/         # GUI headless smoke test
```

`dev/go` runs `golang:1.27.3` with the repo mounted at `/w` and forwards `GOOS`/`GOARCH`/`CGO_ENABLED`.
Host is Linux; the product target is **Windows x64 only** (pure Go — no cgo, no MinGW, no Node/WebView2).
The Windows binary embeds `cmd/sigildns/sigildns.manifest` (`requireAdministrator`), so it requests
admin/UAC on every launch.

## Environment gotchas
- `./dev/go build ./...` occasionally fails on a cold container/module cache. **Re-run** — a single FAIL is not proof.
- `pwsh` is not installed on the host, so `installer/*.ps1` and `scripts/*.ps1` cannot be syntax-checked locally; they are only parsed for real on CI.
- Windows-only code/tests never execute locally; `GOOS=windows ... vet` is the strongest local check.
- The GUI software renderer has no fonts in the container, so headless text assertions are shape/colour-based.
- `gh run list` must be filtered with **`--workflow CI`**: the auto dependency-graph workflow shares `headSha` and otherwise matches.
- `integration-windows` depends on real Quad9 DoH and is **occasionally flaky** (transient `upstream unreachable`); `gh run rerun <id> --failed` once before believing it.

## Architecture (the non-obvious parts)
One binary, entrypoint `cmd/sigildns/main.go`, many modes: CLI commands, `--service` (SCM host),
`--apply <path>` (elevated headless helper), `--gui`, `--tray` (retired stub; the tray now lives in the GUI).

`internal/`: `lifecycle` (enable/disable/restore orchestration) · `state` (atomic JSON + dirty marker +
single-instance lock) · `intercept` (NRPT via PowerShell; the per-adapter fallback is described in
SPEC §4.2 but **not implemented**) · `resolver`
(embedded dnsproxy + filter + per-domain rules) · `filter` (urlfilter wrapper) · `providers` (presets) ·
`config` (YAML) · `ipc` (dual named pipes) · `service` (SCM host) · `drift` (registry watch + reconcile) ·
`elevate` (one-shot UAC helper + allowlisted config patch) · `gui` (`gui` tag, go-gui **config client**,
five-page Status/Settings/Rules/Logging/About surface with an opt-in tray) ·
plus `logging`/`winacl`/`redact`/`version`/`health`.

- **Import cycle:** `internal/config` imports `internal/resolver`, so `internal/resolver` must NOT import
  `internal/config` (use the local `resolver.Rule` type).
- **Platform split** is `_windows.go` / `_other.go` build-tagged pairs. Every Windows implementation has a
  non-Windows stub returning a typed error so Linux builds stay green — add both when adding a feature.
- **Build tags:** `gui` = the go-gui window (including its opt-in tray icon); `tray` = retired stub
  (leave it). Keep default/Linux builds GUI-free by keeping the `gui` tag.
- **GUI is a full config client**, not a status viewer (`internal/gui/`): `settings_view.go` (Settings
  page: service card + profile list add/rename/delete/set-active + per-profile editor), `draft.go`
  (editor buffer), `elevate_adapter.go` (**the only `internal/elevate` importer**; maps GUI actions to
  `elevate.Request`), `instance.go` (`gui.lock` single-instance guard), `tray.go` (opt-in
  go-gui system tray: off by default, plus close-to-tray / minimize-to-tray switches and a
  start-at-sign-in switch that registers a per-user `ONLOGON` Task Scheduler task (`SigilDNS`, task
  XML: logon trigger and principal bound to this user, `InteractiveToken`/`HighestAvailable`,
  battery-start enabled, no run-time limit) launching `--tray` (no UAC at
  sign-in — see the §15.3.8 exception; removed when the tray is disabled and by uninstall); single/double click shows the
  window, right click offers Open / live service status / Open log file / Clear DNS cache / Exit),
  `render_test.go` (headless shape/colour assertions — no fonts in the container),
  `console_{windows,other}.go` (`detachConsole`).
  The GUI is the **full configuration surface**; the default/Linux build stays GUI-free behind the `gui` tag.
- **Elevated-helper allowlist** (`internal/elevate`): `Patch` is the whole config surface —
  `ActiveProfile` / per-profile `Profiles` (provider/variant/protocol/id/url/bootstrap/
  filtering/cache) plus structural `AddProfile` / `RenameProfile` / `DeleteProfile`; actions also include
  `service_install|service_uninstall|service_start|service_stop` and the no-patch `refresh_lists` /
  `clear_cache`. Every field is validated (bootstrap as
  IP literals, provider/variant/protocol against the registry, id charset/length, explicit-zero
  rejection); ids travel only in the DPAPI-sealed body; blank id = keep. The only control-pipe methods
  are `reload` / `enable` / `disable` / `restore` / `refresh_lists` / `clear_cache`; each is classified
  in the §15.3 privilege table.
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
  10 MB cap, newest 3 MB kept). `cmd/sigildns` `loggerFor()` is the only file-log opener;
  `cmdServiceHost` resolves the config once at startup and reuses it for both the logger and the
  lifecycle. The redacting handler (seeded via `providers.ExtractIDs`, **re-seeded on every reload**)
  scrubs resolver
  ids (DoH-path and DoT/DoQ hostname label), URL userinfo/query and `sdns://` at all levels, while
  `debug` exposes query names. The GUI **tails the file directly** — no IPC method. The file is opened
  **without `O_APPEND`** so the handle keeps `FILE_WRITE_DATA`; rotation truncates through that open
  handle (`file.Truncate(0)` + seek/write). `os.Truncate` **by path** is only the GUI's "Clear log file".

## Invariants — do not break
- **A running service always has interception on.** Service start always enables; the SCM state stays
  StartPending until recovery + enable succeed. Any startup/reload/drift failure stops the service
  instead of leaving it running with protection off.
- **Enable order:** bind listeners → two-stage health check → persist state + dirty marker → apply
  interception (NRPT) → flush DNS. Never touch DNS settings before the resolver is proven listening.
- **Never re-snapshot while dirty** — applies only if/when the per-adapter fallback (SPEC §4.2) is
  implemented: restore from the persisted snapshot first; re-snapshotting a loopbacked system
  captures `127.0.0.1` as the "original" and loses the user's real DNS. The current NRPT build stores
  no adapter snapshot.
- **`restore` must work with a corrupt/missing config** (config-independent) — recovery can never depend on YAML.
- **The service runs as `LocalSystem`** (deliberate: a virtual account cannot create the pipe instances, is
  locked out of its own state/config, and lacks NRPT/admin rights). Do not "restore" it to `NT SERVICE\...`.
- **Config and state are ACLed to SYSTEM + Administrators** (`internal/winacl`, applied by `config.Save`);
  ACL failure is a hard error, not best-effort.
- `disable`/`restore`/`status` deliberately use a **config-independent** lifecycle.

## Security rules (SPEC §15.3)
- Resolver ids live only in admin-only files (`config.yaml` and the ACLed `state.json`). **Never** put them on argv/env or send them over IPC;
  the redacting `redact.Handler` keeps them out of logs at every level. `debug` is the deliberate
  exception for query names (not credentials). The status pipe carries only sanitized DTOs.
- Control pipe `\\.\pipe\sigildns` = SYSTEM + Administrators only. Status pipe
  `\\.\pipe\sigildns-status` = read-only, sanitized. Never grant `AU` access to the control pipe.
- **Service starts are registration-verified**: `service.Start` compares the SCM `BinaryPathName`
  against the current executable (with the GUI→CLI sibling rule) before starting. The CLI and the GUI
  share it, so neither bypasses the check; `elevate` keeps no duplicate start path.
- Elevation is explicit (`elevate.Apply` → `ShellExecuteExW runas` → `--apply`). The shipped exe also
  embeds a `requireAdministrator` manifest (see above), so it is elevated for its lifetime; the
  `--apply` helper stays one-shot per write and re-validates everything, applying only the allowlisted
  config surface (see Architecture). No persistent broker. The one silent-start exception is the opt-in
  tray autostart: a per-user `ONLOGON` task (`SigilDNS`, task XML with a user-bound logon trigger,
  `InteractiveToken` and `HighestAvailable`) created by the already-elevated GUI launches `--tray` at sign-in with no UAC — SPEC §15.1/§15.3.8, user
  decision. It starts the GUI only; it never applies config or starts the service.

## CI & releases
- `.github/workflows/ci.yml`: `main` pushes + PRs only, docs-only paths ignored (a tag push must not
  re-run it), a `concurrency` group cancelling superseded PR runs only, ubuntu+windows matrix (`go build`,
  `-tags tray`, `-tags gui`, `vet`, `test`; GUI smoke test on both runners), manifest-resource freshness
  and `-race` checks on Linux, a PowerShell parse check on Windows, plus `integration-windows` (real NRPT
  via `scripts/ci-windows-integration.ps1`). Actions are pinned to commit SHAs (Dependabot keeps them).
- `.github/workflows/release.yml`: tag `v*` or `workflow_dispatch`; builds windows/amd64; reuses a green
  `ci.yml` run for the tagged commit and skips its duplicated vet/test steps (falls back to running them
  only when the SHA has no CI run at all; a red or still-running run blocks the release); ships the
  `docs/` folder, `SPEC.md`, and `CHANGELOG.md`; verifies every expected file and the checksum
  before upload; signs with
  `signtool` only when `WINDOWS_CERT_PFX_BASE64` + `WINDOWS_CERT_PASSWORD` secrets exist (else skip +
  `::warning::`); zips (+ `SHA256SUMS.txt`) and publishes the Release **draft-first** (create draft →
  upload assets → `--draft=false`), so a failed run never exposes an assetless release. Notes come from
  `release-notes/<tag>.md` if present, else the matching `CHANGELOG.md` section, else `--generate-notes`.
  **The GUI exists only with `-tags gui`**, so any
  packaging step must pass it to ship a GUI binary.
- Always use `go-version-file: go.mod` in workflows — never `go-version: '1.27.x'` (setup-go v7 sets
  `GOTOOLCHAIN=local` and will not auto-fetch the toolchain `go.mod` requires).
- After pushing, confirm CI with `gh run watch <id> --exit-status` before calling it green. Commit style:
  imperative subject, no trailers.

## Pinned dependencies (why)
`AdguardTeam/dnsproxy v0.86.0` (engine; needs Go ≥1.26.8; **no `Config.BootstrapDNS` field** — bootstrap is
`upstream.Options.Bootstrap`) · `AdguardTeam/urlfilter v0.23.4` (**GPL-3.0**) · `go-gui-org/go-gui v0.89.1`
(GUI + built-in system tray; pre-1.0, pin deliberately; its Win32 tray menu resolves a click
through the root-prefixed `menuNodes` slice, so `internal/gui/tray.go` shifts the menu IDs by one,
and it never sets `NIF_SHOWTIP`, so `internal/gui/tray_tooltip_windows.go` enables the standard
tooltip — re-check both on upgrade) · `Microsoft/go-winio v0.6.4` (**deliberately pinned** — v0.6.3
historically dragged a `golang.org/x/*` upgrade cascade, so re-check on upgrade). Verify library APIs
against the pinned tag's source, not against docs.
