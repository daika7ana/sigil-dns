# AGENTS.md

Windows 11 system-wide encrypted DNS client (YogaDNS-class, no kernel driver).
Go module `github.com/daika7ana/sigil-dns`, GPLv3 (it statically links
`AdguardTeam/urlfilter`).

`SPEC.md` is the **authoritative design** — read §15 (GUI/privilege), §4.7 (IPC
security), §10 (packaging) before changing behaviour. `README.md` +
`docs/{CONFIG,LIMITATIONS,TROUBLESHOOTING,RELEASE}.md` are user-facing.

## Commands (there is no Go toolchain on the host)
Use the Docker wrapper `dev/go` — never call `go` directly, never install a toolchain.
It runs `golang:1.27.3` with the repo at `/w` and forwards `GOOS`/`GOARCH`/`CGO_ENABLED`.

```sh
./dev/go build ./...                            # default build (GUI-free)
./dev/go build -tags gui ./...                  # include the GUI
./dev/go build -tags tray ./...                 # build-only stub; CI builds it
GOOS=windows GOARCH=amd64 ./dev/go build ./...  # real cross-compile (pure Go)
./dev/go generate ./cmd/sigildns                # regenerate the manifest + icon + PE version-info resource
./dev/go fmt ./internal/...                     # gofmt (fmt is a `go` subcommand)
./dev/go vet ./...
./dev/go vet -tags gui ./...                    # CI runs this too
GOOS=windows GOARCH=amd64 ./dev/go vet ./...    # also compiles Windows-only _test.go files
./dev/go test ./...
./dev/go test -run TestName ./internal/ipc/     # single package / single test
./dev/go test -tags gui ./internal/gui/         # GUI headless smoke test
./dev/go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...  # CI gate (blocking)
```

Host is Linux; the product target is **Windows x64 only** (pure Go — no cgo, no MinGW,
no Node/WebView2). The Windows binary embeds `cmd/sigildns/sigildns.manifest`
(`requireAdministrator`), so it requests admin/UAC on every launch.

## Environment gotchas
- `dev/go` mounts **only the repo** (`/w`) plus the Go caches — the host `/tmp` is not
  visible. A throwaway program (e.g. a manual endpoint probe) must live inside the repo
  and be deleted afterwards, or `go run /tmp/x.go` fails.
- `./dev/go build ./...` occasionally fails on a cold container/module cache. **Re-run** —
  a single FAIL is not proof.
- `pwsh` is not installed on the host, so `install.ps1` and `scripts/*.ps1` cannot be
  syntax-checked locally; they are only parsed for real on CI (Windows runner).
- Windows-only code/tests never execute locally; `GOOS=windows ... vet` is the strongest
  local check.
- The GUI software renderer has no fonts in the container, so headless text assertions
  are shape/colour-based.
- The checked-in `cmd/sigildns/rsrc_windows_amd64.syso` is what embeds
  `requireAdministrator`, the icon, and the PE VERSIONINFO from
  `cmd/sigildns/versioninfo.json` (publisher metadata is an antivirus-heuristic
  signal; `release.yml` stamps the tag version into the JSON, then regenerates).
  If you edit `sigildns.manifest` or `versioninfo.json`, run
  `./dev/go generate ./cmd/sigildns` and commit the `.syso` — CI regenerates it and fails
  on a diff. The generator is `josephspurrier/goversioninfo` (pinned in the
  directive, not in `go.mod`); `rsrc` has no version-info support.
- `gh run list` must be filtered with **`--workflow CI`**: the auto dependency-graph
  workflow shares `headSha` and otherwise matches.
- `integration-windows` depends on real Quad9 DoH and is **occasionally flaky**
  (transient `upstream unreachable`); `gh run rerun <id> --failed` once before believing it.

## Architecture (the non-obvious parts)
One binary, entrypoint `cmd/sigildns/main.go`, many modes: CLI commands, `--service` (SCM
host), `--apply <path>` (elevated headless helper), `--gui`, `--tray` (the native tray host
with no window/GL).

`internal/`: `lifecycle` (enable/disable/restore orchestration) · `state` (atomic JSON +
dirty marker + single-instance lock) · `intercept` (NRPT via PowerShell; the per-adapter
fallback in SPEC §4.2 is **not implemented**) · `resolver` (embedded dnsproxy + filter +
per-domain rules) · `filter` (urlfilter wrapper) · `providers` (presets) · `config` (YAML) ·
`ipc` (dual named pipes) · `service` (SCM host) · `drift` (registry watch + reconcile) ·
`elevate` (one-shot UAC helper + allowlisted config patch) · `gui` (`gui` tag, go-gui
**config client**, five pages Status/Settings/Rules/Logging/About, plus a separate opt-in
tray host) · `logging`/`winacl`/`redact`/`version`/`health`.

- **Import cycle:** `internal/config` imports `internal/resolver`, so `internal/resolver`
  must NOT import `internal/config` (use the local `resolver.Rule` type).
- **Platform split** is `_windows.go` / `_other.go` build-tagged pairs. Every Windows
  implementation has a non-Windows stub returning a typed error so Linux builds stay green
  — add both when adding a feature.
- **Build tags:** `gui` = the go-gui window, the GUI config surface and the opt-in native
  tray host; `tray` = retired stub (`internal/tray`, `ErrNotBuilt`) that must keep
  compiling — leave it.
- **GUI** (`internal/gui/`): `settings_view.go` (Settings page: service card + profile list
  add/rename/delete/set-active + per-profile editor), `draft.go` (editor buffer;
  `expansion()` is the one place that expands the draft provider/variant/protocol/id),
  `elevate_adapter.go` (**the only `internal/elevate` importer**; maps GUI actions to
  `elevate.Request`), `instance.go` (`gui.lock` single-instance guard), `view.go` (Status
  dashboard, About cards), `update.go` (SHA256SUMS-verified release download),
  `status_graph.go`, `copy.go` (**alert-copy boundary**: title + one-sentence body),
  `console_{windows,other}.go` (`detachConsole`), `render_test.go` (headless assertions).
  - Tray: `tray.go` (settings/menu: off by default, close-to-tray, minimize-to-tray,
    start-at-sign-in), `autostart_windows.go` (the per-user `ONLOGON` Task Scheduler task
    `SigilDNS`: user-bound trigger, `InteractiveToken`, `HighestAvailable`, launching
    `--tray` with no UAC at sign-in — SPEC §15.3.8 exception; removed when the tray is
    disabled and by uninstall), `trayhost_windows.go` (**separate no-window tray process**:
    `sni.Tray` icon/menu, 5 s status poll; a message-only control window is its
    single-instance guard and exit channel; ~25–30 MB, so the GUI exits on close /
    minimize-to-tray instead of keeping the OpenGL window process alive).
- **Icon art** lives in the top-level `assets` package (`//go:embed appicon-*.png`;
  per-size frames plus the 256 px master because the GL backend cannot antialias a vector
  mark). The exe resource icon is `assets/sigildns.ico`.
- **Provider presets** (`internal/providers/providers.go`): endpoints are hardcoded from
  SPEC §7 — **do not re-research them online**. `Registry()` is the single source of truth
  for `Expand`, the CLI listing and the GUI menus. `Provider.Protocols` is load-bearing:
  `Expand` rejects a protocol the provider does not publish, and the GUI offers only that
  list. Adding a provider means: constants + registry entry + `expandX` + `Expand` case +
  GUI `providerLabel`/`variantLabels` + tests; a provider with a private id also needs
  `providerForHost` + `extractOne` (redaction, `id_set`). `TestRegistryProtocolsExpand`
  fails if an advertised protocol does not expand to a URL of that transport.
- **Elevated-helper allowlist** (`internal/elevate`): `Patch` is the whole config surface —
  `ActiveProfile` / per-profile `Profiles` (provider/variant/protocol/id/url/bootstrap/
  filtering/cache) plus `AddProfile`/`RenameProfile`/`DeleteProfile`; actions also include
  `service_install|service_uninstall|service_start|service_stop` and the no-patch
  `refresh_lists`/`clear_cache`. Every field is validated (bootstrap as IP literals,
  provider/variant/protocol against the registry, id charset/length, explicit-zero
  rejection); ids travel only in the DPAPI-sealed body; blank id = keep. The control pipe
  has only `reload`/`enable`/`disable`/`restore`/`refresh_lists`/`clear_cache`, each
  classified in the §15.3 privilege table.
  - An **explicit empty** patch bootstrap clears the list; `normalize()` only defaults a
    `nil` one. That is how a bootstrap-free provider (OpenBLD, NextDNS `ultralow`) stays
    bootstrap-free — keep `slices.Clone`, not `append([]string(nil), …)`.
- **Uninstall is ordered and fail-closed** (`uninstallPlan`/`uninstallSequence`): restore
  interception → stop the service → verify interception is actually down (NRPT probe + one
  config-independent restore retry) → only then delete.
- **Config path hardening** (`internal/config/system_windows.go`): `SystemPath`/`SystemDir`/
  `SystemStatePath` resolve `%ProgramData%` via the KnownFolder API, never the env var, with
  no user-dir fallback; service, helper and CLI all use these.
- **`providers.ExtractID`** recovers a resolver id from generated upstreams (provider +
  protocol aware; `ErrNoID`/`ErrAmbiguousID`); the helper uses it for replace-only id
  recovery and `id_set` detection.
- **IPC `ProfileSummary`** (`internal/ipc/summary.go`): sanitized per-profile view (name /
  active / labels / `id_set` / filtering+cache flags+sizes / counts / bootstrap literals) —
  never ids or URLs.
- **Logging** (`internal/logging` + `redact.Handler`): opt-in service-owned file log from the
  top-level `log:` block (default `off`; `level` off|error|info|debug; 10 MB cap, newest
  3 MB kept). `cmd/sigildns` `loggerFor()` is the only file-log opener. The redacting
  handler (seeded via `providers.ExtractIDs`, **re-seeded on every reload**) scrubs ids,
  URL userinfo/query and `sdns://` at all levels; `debug` deliberately exposes query names.
  The GUI **tails the file directly** — no IPC method. The file is opened **without
  `O_APPEND`** so the handle keeps `FILE_WRITE_DATA`; rotation truncates through that open
  handle. `os.Truncate` **by path** is only the GUI's "Clear log file".

## Invariants — do not break
- **A running service always has interception on.** Service start always enables; the SCM
  state stays StartPending until recovery + enable succeed. Any startup/reload/drift
  failure stops the service instead of leaving it running with protection off.
- **Enable order:** bind listeners → two-stage health check → persist state + dirty marker
  → apply interception (NRPT) → flush DNS. Never touch DNS settings before the resolver is
  proven listening.
- **Never re-snapshot while dirty** — applies only if/when the per-adapter fallback
  (SPEC §4.2) is implemented: restore from the persisted snapshot first; re-snapshotting a
  loopbacked system captures `127.0.0.1` as the "original" and loses the user's real DNS.
  The current NRPT build stores no adapter snapshot.
- **`restore` must work with a corrupt/missing config** (config-independent).
- **The service runs as `LocalSystem`** (deliberate: a virtual account cannot create the
  pipe instances, is locked out of its own state/config, and lacks NRPT/admin rights). Do
  not "restore" it to `NT SERVICE\...`.
- **Config and state are ACLed to SYSTEM + Administrators** (`internal/winacl`, applied by
  `config.Save`); ACL failure is a hard error, not best-effort.
- `disable`/`restore`/`status` deliberately use a **config-independent** lifecycle.

## Security rules (SPEC §15.3)
- Resolver ids live only in admin-only files (`config.yaml` and the ACLed `state.json`).
  **Never** put them on argv/env or send them over IPC; the redacting handler keeps them out
  of logs at every level. The status pipe carries only sanitized DTOs.
- Control pipe `\\.\pipe\sigildns` = SYSTEM + Administrators only. Status pipe
  `\\.\pipe\sigildns-status` = read-only, sanitized. Never grant `AU` access to the control
  pipe.
- **Service starts are registration-verified**: `service.Start` compares the SCM
  `BinaryPathName` against the current executable (with the GUI→CLI sibling rule) before
  starting; neither CLI nor GUI bypasses it.
- Elevation is explicit (`elevate.Apply` → `ShellExecuteExW runas` → `--apply`). The shipped
  exe also embeds a `requireAdministrator` manifest, so it is elevated for its lifetime; the
  `--apply` helper stays one-shot per write and re-validates everything. No persistent
  broker. The one silent-start exception is the opt-in tray autostart (`ONLOGON` task,
  above): it starts the tray host only — never applies config or starts the service.

## CI & releases
- `.github/workflows/ci.yml`: `main` pushes + PRs only, docs-only paths ignored (a tag push
  must not re-run it), `concurrency` cancels superseded PR runs only. Matrix
  ubuntu+windows: `build`, `-tags tray`, `-tags gui`, `vet`, `vet -tags gui`, `test`
  (`-timeout 120s`), GUI smoke test on both runners, `-race` on Linux, PowerShell parse
  check on Windows, plus `integration-windows` (real NRPT via
  `scripts/ci-windows-integration.ps1`).
- **Blocking Linux gates beyond `go test`:** `staticcheck@v0.8.2` and
  `govulncheck@v1.9.0`, plus a manifest-resource freshness check (`go generate` → `git diff
  --exit-code` on the `.syso`). Run staticcheck locally before pushing.
- `.github/workflows/release.yml`: tag `v*` or `workflow_dispatch`; builds windows/amd64;
  reuses a green `ci.yml` run for the tagged commit (falls back to running vet/test only
  when that SHA has no run; a red or running run blocks). Ships `docs/`, `SPEC.md`,
  `CHANGELOG.md`; verifies every expected file and the checksum before upload; signs only
  when `WINDOWS_CERT_PFX_BASE64` + `WINDOWS_CERT_PASSWORD` secrets exist (else skip +
  `::warning::`); zips (+ `SHA256SUMS.txt`) and publishes **draft-first** (create draft →
  upload → `--draft=false`). Notes come from `release-notes/<tag>.md`, else the matching
  `CHANGELOG.md` section, else `--generate-notes`.
- **The GUI exists only with `-tags gui`** — any packaging step must pass it to ship a GUI
  binary.
- Always use `go-version-file: go.mod` in workflows — never `go-version: '1.27.x'`
  (setup-go v7 sets `GOTOOLCHAIN=local` and will not fetch the toolchain `go.mod` requires).
- After pushing, confirm CI with `gh run watch <id> --exit-status` before calling it green.
  Commit style: imperative subject, no trailers.

## Pinned dependencies (why)
`AdguardTeam/dnsproxy v0.86.0` (engine; needs Go ≥1.26.8; **no `Config.BootstrapDNS`** —
bootstrap is `upstream.Options.Bootstrap`, and `Upstream.Exchange` takes
`(ctx, *dns.Msg)` — check the pinned tag's source, not docs) ·
`AdguardTeam/urlfilter v0.23.4` (**GPL-3.0**) · `go-gui-org/go-gui v0.92.0` (GUI + tray
backend; pre-1.0, pin deliberately; its Win32 tray menu resolves a click through the
root-prefixed `menuNodes` slice, so `internal/gui/tray.go` shifts the menu IDs by one, and
it never sets `NIF_SHOWTIP`, so `tray_tooltip_windows.go` enables the standard tooltip —
re-check both on upgrade; `trayhost_windows.go` uses the public `gui/backend/sni` API
directly) · `Microsoft/go-winio v0.6.4` (**deliberately pinned** — v0.6.3 historically
dragged a `golang.org/x/*` upgrade cascade, so re-check on upgrade).
