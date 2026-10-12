# GUI

`dns-client-gui.exe` is the full configuration client for the service. It has a
**Status** page (banner + a single power control + profile / filtering / cache
cards) and a **Settings** page (service install / remove, profile management,
and a per-profile editor), without a console. The binary embeds an application
manifest that requests **administrator**, so Windows shows one UAC prompt at
**launch** and the window runs elevated until you close it.

The GUI is a **client of the service**: it holds no DNS logic and never edits
`config.yaml` itself — writes go through the elevated `--apply` helper. Install
and start the service first (see the [installer](RELEASE.md) or
`dns-client service install` + `service start`); without it the window stays in
the "Not running" state. A `gui.lock` beside `gui.json` keeps a single instance
running.

## Launching

- **Start Menu** — the packaged installer creates a shortcut named
  `dns-client` that targets `dns-client-gui.exe`.
- **Directly** — run `dns-client-gui.exe` with **no arguments**. The GUI binary
  is built with `-ldflags "-H windowsgui"`, so it opens the window and there is
  no console behind it. `--gui` opens the window explicitly.

## Pages

**Status** polls the service about every 2 seconds and shows:

- A banner for the overall state.
- A single header power control — **Enable** / **Disable** (and **Restore** only
  for a crash-leftover dirty state).
- Cards for the active profile, filtering, and cache. Provider, variant, and
  protocol appear as **labels** (`cloudflare / security / doh`); the resolver id
  is **masked** — `•••• (set)` or `not set`, never the value.

**Settings** shows:

- A **Service** card: installed state + install / remove.
- A **Profiles** list: select the active profile, and add / rename / delete.
- A **profile editor**: provider / variant / protocol, the resolver id (masked;
  leaving it blank keeps the existing one), a custom URL, bootstrap IPs,
  filtering + block response, and cache + size.

## States

| State | Meaning |
|---|---|
| **Protected** | The service is running and interception is applied. |
| **Not running** | The service is stopped. |
| **Restore needed** | A dirty marker from a previous session — interception was applied but not cleanly restored. |
| **Needs attention** | The service is running but degraded: the config could not be loaded, or the health check failed. |
| **Unverified** | The status pipe's server process is not the dns-client service. Reads are not trusted and all actions are refused. |

## Controls

| Control | Shown when | Action |
|---|---|---|
| **Enable / Disable** (header power) | service installed | Start interception (and the service) / stop it. |
| **Restore now** | Restore needed / Needs attention | Clean up stale interception. |
| **Install / Uninstall service** | Settings | Register or remove the Windows service. |
| **Add / Rename / Delete profile** | Settings | Manage profiles (delete is refused for the active profile). |
| **Save** | Settings, draft dirty | Apply the profile editor via the elevated helper. |
| **Refresh** | always | Re-read status immediately. |
| **Theme** | always | Toggle Auto / Light / Dark. |

Because the window already runs elevated (one UAC prompt at launch, not per
action), pressing an action does **not** raise a second UAC prompt: the write goes
through the `--apply` helper, which still re-validates everything. The trade-off
is that the window holds an admin token for its whole lifetime.

## Security model

These are the SPEC §15 rules as the window enforces them:

- **Sanitized reads.** Reads go over the read-only status pipe
  (`\\.\pipe\dns-client-status`), which carries only sanitized data — labels,
  flags, counts, hosts, and non-secret IP literals. It never carries resolver
  ids or credential-bearing URLs.
- **Admin-only actions.** Actions go over the control pipe
  (`\\.\pipe\dns-client`, SYSTEM + Administrators only), through the `--apply`
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

Window geometry and the theme preference live in
`%LOCALAPPDATA%\dns-client\gui.json` (user scope, never secrets). Delete that
file to reset the window size and theme to defaults.
