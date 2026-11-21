# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security
- Resolver ids can no longer leak into IPC error messages (and from there into the GUI) when logging is off, the default: the scrubbing boundary is now always seeded.
- A standard user who pre-creates the data directory can no longer plant a hardlink as the lock or log file to make the SYSTEM service truncate or re-ACL an arbitrary file; multiply-linked files are refused before any write.
- Filter-list sources must now be **HTTPS** URLs or absolute drive paths; plain `http://` is rejected. A plaintext list is an answer-injection channel (`$dnsrewrite`), and every other path in the product is encrypted. **Breaking:** configs with `http://` list URLs now fail to load — move the list to HTTPS.
- The elevated helper's verdict file is now DPAPI-sealed, so a process running as the same user cannot forge a "success" result the GUI trusts.

### Fixed
- Boot starts no longer fail while Windows is still bringing the network up: the upstream health check is retried for about 10 s (2 s apart) while our own listener is the one answering. A foreign owner of :53 still fails at once.
- A start that races a shutdown now stops without enabling, instead of leaving a rule pointed at a resolver that is about to disappear.
- Shutdown restores DNS. The service takes the pre-shutdown notification, which Windows sends while the DNS client and CIM are still alive, and restores within 8 s (the SCM allows 10 s). Previously only the late shutdown notification arrived, so the rule survived the reboot.
- The Status header no longer offers **Restore now** while the service is stopped: it restored DNS but never started the service, so a second click repeated the message. A stopped service now offers **Turn on protection**; **Restore now** stays for a crash leftover.
- Hand-written provider labels (free-form per docs/CONFIG.md, e.g. `NextDNS`) no longer break every GUI save: an unknown label is kept as display text instead of failing the write, and a custom profile without a typed URL no longer gets a protocol error for a control that is not on screen.
- A zero-byte or wiped `config.yaml` now fails loudly instead of silently enabling the default profile.
- Drift supervision no longer goes blind: a missing state file now counts as a probe failure and three consecutive failures stop the service (which restores DNS) rather than continuing unprotected; the NRPT probe is also time-bounded like every other PowerShell call, and a foreign NRPT rule no longer hides a wedged resolver.
- A pre-shutdown request that arrives during a drift stop now gets its 8 s restore budget instead of an unbounded restore that Windows kills mid-way.
- A control pipe that stops accepting no longer leaves the service Running but uncontrollable: the service now restarts and rebinds.
- A failed restore on a user stop is retried once under a bound, and the log now names `sigildns restore` as the recovery.
- Renaming a profile no longer re-applies a stale editor buffer over a config the service just changed, or silently drops edits on a legacy summary; an untouched editor now refreshes from the live config instead of reading as edited.
- `gui.json` is written atomically, so a crash mid-write can no longer reset your preferences or leave an orphaned tray task at sign-in.
- A second download of the same release no longer deletes the verified file when the plain name is held open; it falls back to `<name>-1.zip` and up.
- The GUI no longer reports "Resolver ID: set" for presets that have no private id (ControlD free, for example).

### Changed
- The plain-DNS leak probe runs alongside the enable health check instead of after it, so a slow PowerShell/WMI probe no longer adds its full runtime. It is still awaited before the state is saved (bounded at 5 s).
- The status pipe now allows 64 connections with a 2 s first-read deadline, so one misbehaving local process cannot starve the tray's status view.
- A per-domain rule domain is validated with the same parser the service uses at reload, so an invalid value fails the save instead of stopping the service later.
- Pre-release tags (e.g. `v1.2.3-rc.1`) now stamp the exe's fixed numeric version fields instead of leaving the committed 0.7.1.

## [0.8.2] - 2026-11-18

### Fixed
- The tray icon no longer fails silently at sign-in after an upgrade. `service uninstall` deletes the per-user `SigilDNS` logon task while the setting stays on in `gui.json`, so sign-in launched nothing; the GUI now re-registers the task at startup when the setting is on, which also refreshes the exe path after an upgrade. One GUI launch after upgrading restores it.
- A brief upstream stall no longer counts as an unhealthy resolver. The stage-B health probe's fixed 5 s budget was shorter than a real fallback-answered resolution, so a success was recorded as `upstream unreachable` and three such stalls auto-disabled interception; it now outlasts the failover plus fallback budget when a fallback profile is configured.

## [0.8.1] - 2026-11-17

### Fixed
- `%ProgramData%\SigilDNS` is now owned by Administrators, not by whoever created it, so a standard user who pre-created the folder can no longer re-ACL it and read resolver ids (an existing foreign-owned directory is taken over; delete or re-own it first if that must not happen).
- The log path must now be inside `%ProgramData%\SigilDNS`, so a config edit can no longer point the SYSTEM service at a file it would re-ACL and truncate on rotation, such as the hosts file.
- Filter-list sources must now be HTTP(S) URLs or absolute drive paths; UNC and device paths are rejected so a patch cannot make the SYSTEM service authenticate out or read arbitrary files.
- The elevated `--apply` helper validates the staging request path before any write, so an arbitrary argument can no longer delete or replace an unrelated `.res` file.
- The GUI's Open/Clear log file actions need a verified status read instead of the user-writable cache, so a same-user process cannot point them at an executable or an arbitrary file to truncate.
- The logon-task XML is staged in the admin-only data directory instead of `%TEMP%`, closing a race where a same-user process could swap in an arbitrary task command.
- Concurrent admin config edits are serialized with a lock, so two simultaneous applies can no longer silently drop each other's change.
- Config/state saves now apply the ACL before the rename and retry a transient Windows rename sharing violation, so a failed ACL step cannot leave a rejected change on disk and a file scanner cannot break a save.
- The service now creates and secures its data directory before taking the state lock, so a manual install that skips `config init` can still start.
- The reconcile loop now probes resolver health after enable and auto-disables after 3 consecutive failures, so a bound-but-wedged resolver can no longer leave the machine pointed at a dead DNS port while status reports healthy.
- The status read no longer waits on the lifecycle mutex, and a failed bypass re-apply is retried with backoff instead of being frozen until restart.
- `restore` now refuses to run against a live resolver, so one control-pipe call can no longer disarm a running service with no automatic recovery.
- `$dnsrewrite` rules now answer PTR and TXT queries instead of returning an empty answer.
- A rule left behind by a crashed service is no longer deleted before the next enable, so a crash-recovery boot no longer falls back to the adapters' DNS servers while the resolver starts.
- The GUI offers **Restore now** while the service is stopped, so a leftover rule can be cleared without starting it.
- The tray icon no longer vanishes silently at sign-in. The host now waits for Explorer's notification area and retries icon creation instead of exiting after ~4 s with no console, re-adds the icon when Explorer rebuilds it, and reports a mutex failure instead of treating it as "already running".

## [0.8.0] - 2026-11-08

### Fixed
- One broken filter-list source no longer discards the lists that did load, and a source that answers 200 with content that parses to no rules is rejected instead of silently turning filtering off.
- `service uninstall` stops and verifies the service before removing it, so a failed stop cannot leave it running with interception off; uninstall also removes the per-user `SigilDNS` logon task.
- A per-domain rule upstream containing whitespace is rejected instead of silently becoming several load-balanced upstreams.
- Hosts entries that map a name to a real address no longer count as blocked queries.
- The elevated helper reports the cause of early failures (elevation, staging, request, decryption) instead of "did not report a result", and writes its result with `O_EXCL` so a pre-planted link cannot redirect an administrator write.
- Resolver ids are scrubbed from IPC error responses for DoT/DoQ profiles, where the id is the host label.
- Raising the already-open window only accepts a window owned by this executable.

### Changed
- One IPC connection may issue at most 128 requests, so a status-pipe holder cannot occupy every slot indefinitely.
- The unused IPC `disable` method was removed; the CLI `disable` command is unchanged.

## [0.7.1] - 2026-11-06

### Added
- Per-profile failover budgets: `failover_after_seconds` (default 10) caps the active profile's exchange, dnsproxy's own retries included, before the fallback profile is used; `fallback_timeout_seconds` (default 5) caps each fallback upstream's exchange, so a fallback profile with several upstreams can take that long per server. Both sit next to the fallback selector in the profile editor, so a slow primary can be made to fail over sooner.
- A rate-limited `fallback used` line at info level when the fallback answers a query, naming the fallback profile and its duration. It never carries a resolver id, upstream URL, or query name.

### Changed
- The Windows binaries now embed PE version information (product name, publisher, file and product version) alongside the manifest and icon, so Explorer's Properties dialog and the security products that read it see a named, versioned release instead of an anonymous executable; `docs/TROUBLESHOOTING.md` explains how to verify a download flagged by antivirus. A signature is still the only reliable fix.

## [0.7.0] - 2026-11-02

### Added
- **Leak guard.** Enabling now resolves a private probe name through the normal Windows resolver stack and requires our own answer, so "protected" is a verified fact instead of an assumption; a rule that loses to Group Policy, MDM, or a broken DNS Client state makes the enable fail and restores DNS. A foreign catch-all NRPT rule is the exception (SPEC §4.1): the miss is explained, so the service warns and keeps running instead of refusing to start.
- **Leak indicator.** On enable (and on every drift re-apply) Sigil DNS asks the adapters' configured DNS servers directly whether they still answer plain DNS. If they do, an app that ignores the system resolver can bypass Sigil DNS; the result is exposed as `leak` on the status payload and shown as a chip in the Status hero (`DNS leak path open.` / `No DNS leak detected.`); it is the normal case, not a warning, so the headline stays "Protected". The verdict is cleared when a probe cannot run and when interception is disabled.
- Four more preset providers: AdGuard DNS (`adguard`: default, family, unfiltered), Google Public DNS (`google`: unfiltered, DoH/DoH3/DoT), OpenBLD (`openbld`: ada, ric over DoH/DoT, no bootstrap), and uBlockDNS (`ublockdns`: per-profile DoH/DoH3 endpoint that requires an id).
- DoH3 was checked against every provider that offers DoH: Quad9, Cloudflare, AdGuard DNS, Google Public DNS, NextDNS, ControlD, and uBlockDNS answer over HTTP/3; OpenBLD does not.

### Changed
- A provider now advertises the transports it publishes, and an unsupported selection is rejected instead of expanding to an undocumented endpoint (uBlockDNS is DoH/DoH3, OpenBLD DoH/DoT).
- The profile editor offers only the transports the selected provider serves, clamps an unsupported current pick when the provider changes, and clears the stored bootstrap when the provider has none (OpenBLD, NextDNS `ultralow`).

### Fixed
- The profile editor's provider/variant dropdown opens at the width of its field, and the wheel scrolls the open menu instead of the page.

## [0.6.1] - 2026-10-28

### Fixed
- The single-instance lock is now an OS file lock held for the session, so a crash can no longer let two processes both believe they own protection or let one delete the other's lock.
- Reloading the profile keeps the session lock across the old/new swap, its rollback, and an idle reload before the first enable; a failed restore no longer releases it.
- The service state lock file has the same SYSTEM + Administrators ACL as the state file, so its permissions no longer depend on the token that created it.
- Hosts-list blocks can no longer be bypassed with AAAA or HTTPS queries: a matched hosts name is answered locally, with an empty answer for types that have no matching address.
- Enabling refuses a listener on any port other than 53 instead of advertising an unreachable address to Windows; custom ports remain available to the resolver-only run command.
- Stopping the service still restores DNS if its state file was deleted while running, instead of assuming it was never enabled. A failed enable now makes the same attempt even when the state file vanishes while interception is being applied.
- A filtering pause now flushes the resolver and Windows DNS caches when its 15-minute timer expires, not only on an early resume, so an allowed answer cannot outlive the displayed expiry.
- All matching DNS rewrites are applied together: multiple addresses are all returned, and an A rewrite is no longer sent back for an AAAA (or other type) query; a rewritten name with no record for the query type gets an empty answer instead of leaking upstream. IPv4-mapped IPv6 rewrite values are returned as AAAA answers instead of being dropped.
- Filter-list downloads during start and reload use the caller's cancellation, so stopping the service or a timed-out reload no longer waits for the download timeout.
- The GUI log viewer reads only the newest window of a large log instead of allocating the whole file, so opening a multi-gigabyte log cannot exhaust memory.
- A failed resolver start closes the upstreams it already built, including the fallback profile and per-domain rule upstreams, instead of leaving them for the garbage collector.
- A `$dnsrewrite` answer no longer inflates the Status page's blocked count; only actual blocks are counted.

## [0.6.0] - 2026-10-25

### Added
- The Status page shows how many queries this session handled and blocked, plus a **Pause filtering** button that skips filtering for 15 minutes when a site is broken by a list. DNS protection stays on; the button becomes **Resume filtering** while paused, and filtering turns itself back on when the timer runs out.
- Setup registers Sigil DNS in Windows **Add/Remove Programs**, so it can be uninstalled from Settings → Apps as well as from the installer menu.

### Changed
- The Status page cards now share one three-column grid at every window width, so card edges and gaps line up and the layout no longer jumps between sizes.
- Log entries are readable: `[date time][LEVEL] message key=value`, one line per entry, with control characters escaped. A failed PowerShell step reports the one useful error line instead of the whole echoed command.
- Filter-list failures now say what went wrong — not found (404), permission denied (403), timed out, HTML instead of a list — without printing the list's address.
- Resolver IDs are no longer accepted as a command-line value: `providers show` prompts for the id with hidden input (`--id`) or reads it from a file or pipe (`--id-file <path>`, `-` for stdin). A value on the command line is refused. Scripts that passed `--id <value>` must switch to `--id-file`.
- The one-line install command is served from the latest published release instead of the moving `main` branch, so a push to `main` can never change the script it runs.
- Installer paths come from Windows' own known folders rather than environment variables, and setup refuses an install or data folder redirected by a reparse point.

### Fixed
- NextDNS Ultralow stays bootstrap-free: only an omitted bootstrap gets the default, while a deliberately empty list stays empty.
- DNS interception advertises only the loopback addresses the resolver actually bound instead of always claiming IPv6; when another program holds the IPv6 port, Windows clients are no longer sent to it.
- The drift check now compares the actual DNS rules and name servers, so a partly removed rule set or a stale leftover is repaired instead of being missed. A failing bypass rule can no longer turn protection off.
- A corrupt state file is quarantined and replaced with a clean off state, so the app recovers instead of failing to start or leaving protection on with no way to stop it.
- Startup fails closed when old DNS rules cannot be removed, instead of reporting protection on over a half-applied rule set.
- Pausing or resuming filtering clears the DNS cache, and a failed cache flush no longer reports a successful filter-list update as failed.
- Uninstall now stops if interception cannot be proven removed instead of deleting the install anyway; it also closes tray hosts owned by other signed-in users before removing the folder.
- Uninstall removes the per-user autostart task even when it was created by another administrator account.
- Turning the tray on or off no longer freezes the window while the tray starts, cannot leave a stray icon or a duplicate host behind, and switches itself back off with an explanation when the tray cannot start.
- Configuration mistakes are now reported instead of silently ignored: unknown keys, a bootstrap that is not an IP address, a malformed per-domain rule, an explicit `schema_version: 0`, or a zero cache size with the cache enabled. Configs written by 0.1.x still load.

### Security
- The About page's update download is verified against the release's `SHA256SUMS.txt` and refuses a file that does not match or has no checksum entry.
- Resolver IDs stay off the command line and out of logs and error messages, and URLs are scrubbed from status and filter-list errors before they reach the window or the admin channel.

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
