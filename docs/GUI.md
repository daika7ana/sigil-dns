# GUI

`dns-client-gui.exe` is a small read-only status window for the service. It
shows what the resolver is doing and gives you one-click Enable / Disable /
Restore, without a console and without leaving an elevated process running.

The GUI is a **client of the service**: it holds no DNS logic and does nothing
on its own. Install and start the service first (see the [installer](RELEASE.md)
or `dns-client service install` + `service start`); without it the window stays
in the "Not running" state.

## Launching

- **Start Menu** — the packaged installer creates a shortcut named
  `dns-client` that targets `dns-client-gui.exe`.
- **Directly** — run `dns-client-gui.exe` with **no arguments**. The GUI binary
  is built with `-ldflags "-H windowsgui"`, so it opens the window and there is
  no console behind it. `--gui` opens the window explicitly.

## What the window shows

The window polls the service about every 2 seconds. It displays:

- The active profile name.
- Provider, variant, and protocol **labels** (`quad9 / secure / doh`).
- Filtering and cache settings, and rule/list counts.
- Bootstrap IP literals.
- A **masked** resolver id — `•••• (set)` or `not set`, never the value.

## States

| State | Meaning |
|---|---|
| **Protected** | The service is running and interception is applied. |
| **Not running** | The service is stopped. |
| **Restore needed** | A dirty marker from a previous session — interception was applied but not cleanly restored. |
| **Needs attention** | The service is running but degraded: the config could not be loaded, or the health check failed. |
| **Unverified** | The status pipe's server process is not the dns-client service. Reads are not trusted and all actions are refused. |

## Buttons

| Button | Shown when | Action |
|---|---|---|
| **Enable** | Not running | Start interception. |
| **Disable** | Protected | Stop interception. |
| **Restore now** | Restore needed / Needs attention | Clean up stale interception. |
| **Refresh** | always | Re-read status immediately. |
| **Theme** | always | Toggle Auto / Light / Dark. |

Each Enable / Disable / Restore action raises **exactly one** UAC prompt. There
is no persistent elevated process and no broker; elevation is per action.

## Security model

These are the SPEC §15 rules as the window enforces them:

- **Sanitized reads.** Reads go over the read-only status pipe
  (`\\.\pipe\dns-client-status`), which carries only sanitized data — labels,
  flags, counts, hosts, and non-secret IP literals. It never carries resolver
  ids or credential-bearing URLs.
- **Admin-only actions.** Actions go over the control pipe
  (`\\.\pipe\dns-client`, SYSTEM + Administrators only), and only after a
  one-shot elevation.
- **One UAC prompt per action.** Elevation is `ShellExecuteExW` "runas" →
  `--apply <staging>`. The staged request is DPAPI user-scope encrypted and
  deleted after it is read; the helper re-validates everything and applies only
  an allowlisted patch.
- **Verified identity.** The window confirms the pipe server PID matches the
  SCM-reported service PID before trusting reads or sending actions.
- **No user-scope secrets.** `config.yaml` and `state.json` stay ACLed to
  SYSTEM + Administrators; no user-scope file holds secrets.

## Window state

Window geometry and the theme preference live in
`%LOCALAPPDATA%\dns-client\gui.json` (user scope, never secrets). Delete that
file to reset the window size and theme to defaults.
