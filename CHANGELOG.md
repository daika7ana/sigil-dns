# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- The Status page shows this session's query and blocked counts, and can pause filtering for 15 minutes (DNS protection stays on) when a site is broken by a list, with a Resume button to turn it back on early.
- Setup registers Sigil DNS in Windows **Add/Remove Programs**, so it can be uninstalled from Settings → Apps.
- The one-line install command is now fetched from the latest published release's assets instead of the moving `main` branch.

### Fixed
- Uninstalling now stops if interception cannot be proven removed, instead of deleting the install anyway; it also closes tray hosts owned by other signed-in users before removing the folder.
- Uninstall removes the per-user autostart task even when it was created by another administrator account.

## [0.5.0] - 2026-10-24

### Added
- Install with a single PowerShell command; the latest release is downloaded and verified for you.
- HTTP/3 (DoH3) support for providers and custom profiles that offer it.
- The About page now checks for a newer release and can download the right build for you.

### Changed
- One installer now handles install, upgrade, and uninstall, and can make `sigildns` available in every terminal and open the app when done.
- Unattended installs can skip every prompt with `-Quiet`.

### Fixed
- Custom DoT and DoQ upstreams no longer fail when the protocol menu still shows DoH.

## [0.4.0] - 2026-10-24

### Added
- A redesigned Status page that answers the important questions at a glance: whether protection is on, which resolver and protocol you are using, what is being blocked, and whether the cache is ready.
- The About page now shows the version of the service that is actually running, and the author and repository are clickable links.

### Changed
- Filtering on/off and the block response now sit with the filter lists on the Rules page, so all blocking controls are in one place.
- Alerts are shorter and calmer: one sentence of explanation, and routine successes no longer repeat the title.
- When an action fails, the alert now tells you why. A cancelled administrator prompt is reported as cancelled instead of as a failed change.
- The Auto / Light / Dark theme picker moved from the header to Settings → Appearance.
- "Update blocklists" is now "Update filter lists", matching what the button actually downloads.
- Providers that need a resolver ID (for example NextDNS) now say which value to enter, right under the field.
- Icons are drawn at their native size for the title bar, taskbar, tray, header, and About page instead of being scaled down from one large image.

## [0.3.0] - 2026-10-22

### Changed
- When the upstream resolver keeps failing, the log now shows one line per message and upstream every 30 seconds, with a count of what was hidden.
- The tray icon now runs on its own, so closing or minimizing to the tray no longer keeps the window's ~230 MB in memory. The tray itself uses about 25-30 MB.
- Installing or upgrading now closes a running window or tray icon and retries until Windows releases the old executable, instead of failing with a file-in-use error.

## [0.2.0] - 2026-10-22

### Added
- An optional tray icon that shows whether protection is on and offers Open, Open log file, Clear DNS cache, and Exit.
- Settings for minimizing or closing the window to the tray.
- An option to start the tray icon when you sign in.

### Fixed
- The window regains keyboard focus when you reopen it from the tray.
- A failed restore during shutdown is now reported instead of passing silently.
- A leftover lock file no longer stops the app from starting after a crash.

## [0.1.3] - 2026-10-21

Maintenance release with no user-facing changes.

## [0.1.2] - 2026-10-21

### Fixed
- The service no longer reports itself as running when DNS protection did not start. Startup only succeeds once protection is actually in place.

## [0.1.1] - 2026-10-21

### Changed
- Installation and uninstallation now go through a single `setup.ps1` with an Install / Uninstall menu that requests administrator rights itself.

## [0.1.0] - 2026-10-21

### Added
- System-wide encrypted DNS for Windows: a local resolver on `127.0.0.1` and `[::1]`, with Quad9, Cloudflare, NextDNS, ControlD, and custom profiles over DoH, DoT, or DoQ.
- Per-domain routing (send a domain to another resolver, or let it use the system's normal DNS) and blocklists in hosts, AdGuard, and ABP format, answered with `0.0.0.0`/`::` or NXDOMAIN.
- A Windows service that keeps protection on, repairs DNS settings if another program or policy changes them, and restores a clean state after a crash.
- A command line for the same engine: `enable`, `disable`, `restore`, and `status`.
- Opt-in service logging with rotation.
- A five-page desktop app (Status, Settings, Rules, Logging, About) for profiles, rules, filter lists, and service control.
- An installer with a Start Menu shortcut, an optional signed build, and a SHA-256 checksum for every download.
- Automatic fallback to a second resolver if the primary one stops responding, and DNSSEC record requests to upstream resolvers.

### Changed
- The project is now called Sigil DNS (formerly dns-client).

### Fixed
- Uninstall now restores your DNS settings first, verifies they are really restored, and only then removes the service. It refuses to delete anything while protection may still be active.
- Changes sent for administrator approval can no longer touch anything outside the supported settings.

### Security
- Resolver IDs stay in administrator-only files and never appear in the app window, logs, or command line. The status channel carries only sanitized information, and failing to lock down configuration files is treated as an error rather than a warning.
