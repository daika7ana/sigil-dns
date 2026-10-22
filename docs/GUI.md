# GUI

`sigildns-gui.exe` is the full configuration client for the service. It has a
**Status** page (banner + profile / filtering / cache cards), a **Settings** page
(service Start / Stop and install / remove, profile management, and a
per-profile editor), a **Rules** page (filter lists + per-domain routing with a
per-rule Upstream / Bypass kind), a
**Logging** page (level, location, and a live viewer), and an **About** page
(version, IPC protocol, author, repository, license), without a console. The
binary embeds an application
manifest that requests **administrator**, so Windows shows one UAC prompt at
**launch** and the window runs elevated until you close it.

The GUI is a **client of the service**: it holds no DNS logic and never edits
`config.yaml` itself — writes go through the elevated `--apply` helper. Install
and start the service first (see the [installer](RELEASE.md) or
`sigildns service install` + `service start`); without it the window stays in
the "Not running" state. A `gui.lock` beside `gui.json` keeps a single instance
running.

## Launching

- **Start Menu** — the packaged installer creates a shortcut named
  Sigil DNS that targets `sigildns-gui.exe`.
- **Directly** — run `sigildns-gui.exe` with **no arguments**. The GUI binary
  is built with `-ldflags "-H windowsgui"`, so it opens the window and there is
  no console behind it. `--gui` opens the window explicitly.

## Pages

The left nav rail switches between **Status**, **Settings**, **Rules**, **Logging**, and **About**.
**Status** refreshes the live service state about every 1 second while the window is focused and shows:

- A banner for the overall state.
- A **Restore now** header action, shown only for a crash-leftover dirty state.
  Service **Start** / **Stop** lives in the **Settings** service card.
- Cards for the active profile, filtering, and cache. Provider, variant, and
  protocol appear as **labels** (`cloudflare / security / doh`); the resolver id
  is **masked** — `•••• (set)` or `Not set`, never the value.

**Settings** shows:

- A **Service** card: a **Start** / **Stop** control (Start runs the service and
  turns protection on; Stop stops it) plus install / remove; the installed state
  — `Running`, `Stopped`, or `Not installed` — shows as a badge in the header.
- A **Profiles** list: select the active profile, and add / rename / delete.
- A **profile editor**: provider / variant / protocol, the resolver id (masked;
  leaving it blank keeps the existing one), a custom URL, bootstrap IPs,
  filtering + block response, and cache + size.

**Rules** shows:

- The filter-list editor and the per-domain routing rules in one scrollable
  column.
- Each per-domain rule row has a **Rule type** selector — **Upstream** or
  **Bypass**. An **Upstream** row takes the target resolver; a **Bypass** row
  resolves the domains via the system's normal DNS and replaces the input with
  the caption *"Resolved by the system's normal DNS, not by Sigil DNS."* The
  Enable / Remove controls are identical for both kinds.

**Logging** shows:

- A level selector — **Off**, **Errors only**, **Normal**, or **Verbose** — and
  a location field (blank uses the default beside `config.yaml`).
- **Apply** writes the setting through the elevated helper and notes
  *"Takes effect on next service start."*
- A **live viewer** that tails the log file, with a **refresh** slider from
  **0 s (off)** to **30 s**. **Verbose** may record DNS query names, so treat
  the log as sensitive.

**About** shows:

- App name `Sigil DNS`.
- Version — the `internal/version` `Version` value (`dev` unless stamped at
  build time).
- IPC protocol version — currently `1`.
- Author `daika7ana`.
- Repository — <https://github.com/daika7ana/sigil-dns>.
- License GPLv3 (the project statically links `AdguardTeam/urlfilter`, which is
  GPL-3.0).

## Tray

The system tray icon is **off by default**. Settings → **Window & tray** has:

- **System tray icon** — enable the notification-area icon.
- **Start tray icon on startup** — while the tray is on, register a per-user
  Task Scheduler `ONLOGON` task (`SigilDNS`, current user, highest privileges)
  that launches `sigildns-gui.exe --tray` at sign-in. The task starts the tray
  host **elevated with no UAC prompt**; its exe path is pinned to the binary you
  enabled the setting from, so enable it from the installed copy. Turning this
  off deletes the task, turning the tray off deletes it too, and uninstall
  removes it.
- **Minimize to tray** / **Close to tray** — quit the window to the icon instead
  of minimizing or stopping the icon. The icon is a separate native host
  process (~25-30 MB, no window/OpenGL), so the GUI can exit instead of keeping
  a GPU-backed window process alive at ~230 MB. Clicking the icon launches the
  GUI again.

`--tray` runs that host and never opens a window.

## States

| State | Meaning |
|---|---|
| **Protected** | The service is running and interception is applied. |
| **Not running** | The service is stopped. |
| **Restore needed** | A dirty marker from a previous session — interception was applied but not cleanly restored. |
| **Needs attention** | Interception is on but the active config is invalid or the last health check failed. |
| **Unverified** | The status pipe's server process is not the Sigil DNS service. Reads are not trusted and all actions are refused. |

## Controls

| Control | Shown when | Action |
|---|---|---|
| **Start / Stop** (Settings service card) | service installed | Start interception (and the service) / stop it. |
| **Restore now** | Restore needed / Needs attention | Clean up stale interception; stops a running service and verifies the NRPT rule is gone. |
| **Install / Uninstall service** | Settings | Register or remove the Windows service. |
| **Add / Rename / Delete profile** | Settings | Manage profiles (delete is refused for the active profile). |
| **Save** | Settings, draft dirty | Apply the profile editor via the elevated helper. |
| **Apply** | Logging | Save the log level / location for the next service start. |
| **Theme** | always | Toggle Auto / Light / Dark. |

The Status page reads the live service state (SCM state + status DTO only) about
every 1 second while the window is focused, and pauses polling while the window
is unfocused or minimized. The full configuration summary is refreshed at
startup, after your own edits, on a page switch, and on a 30-second safety tick.
An unchanged read repaints nothing, so an idle window costs nothing. There is no
manual **Refresh** button.

Because the window already runs elevated (one UAC prompt at launch, not per
action), pressing an action does **not** raise a second UAC prompt: the write goes
through the `--apply` helper, which still re-validates everything. The trade-off
is that the window holds an admin token for its whole lifetime.

## Security model

These are the SPEC §15 rules as the window enforces them:

- **Sanitized reads.** Reads go over the read-only status pipe
  (`\\.\pipe\sigildns-status`), which carries only sanitized data — labels,
  flags, counts, hosts, and non-secret IP literals. It never carries resolver
  ids or credential-bearing URLs.
- **Admin-only actions.** Actions go over the control pipe
  (`\\.\pipe\sigildns`, SYSTEM + Administrators only), through the `--apply`
  helper, which only a process holding an admin token can drive.
- **Consent at launch, not per action.** The manifest prompts UAC when the exe
  starts; the window is then elevated for its lifetime. Actions still go via
  `ShellExecuteExW` "runas" → `--apply <staging>`: the staged request is DPAPI
  user-scope encrypted and deleted after it is read, and the helper re-validates
  everything and applies only an allowlisted patch.
- **Verified identity.** The window confirms the pipe server PID matches the
  SCM-reported service PID before trusting reads or sending actions.
- **No user-scope secrets.** `config.yaml` and `state.json` stay ACLed to
  SYSTEM + Administrators; no user-scope file holds secrets.

## Window state

The window defaults to 1280 × 720 and cannot be resized below 980 × 620. The
left navigation rail (Status / Settings / Rules / Logging / About) is intentionally narrow.
Window geometry and the theme preference live in
`%LOCALAPPDATA%\SigilDNS\gui.json` (user scope, never secrets). Delete that
file to reset the window size and theme to defaults.
