# Releasing Sigil DNS

This document is for maintainers. It describes how to cut a release, what the
`release` workflow does, and how to configure or bypass code signing.

The user-facing documentation lives elsewhere; this file only covers the release
process.

## What ships

The release zip is `sigildns_<version>_windows_amd64.zip` and contains:

- `sigildns.exe` — the console-subsystem binary: CLI, service host, and the
  headless `--apply` elevation helper.
- `sigildns-gui.exe` — the GUI-subsystem configuration client (Status,
  Settings, Rules, Logging, and About), built `-tags gui -ldflags "-H windowsgui"`.
- `config.example.yaml`, `README.md`, `SPEC.md`, `CHANGELOG.md`, `LICENSE`, and
  `docs/` (the README links into it).
- `installer/setup.ps1` — one entry point; prompts for Install or Uninstall and
  relaunches itself through UAC when started non-elevated.

The GUI update checker matches assets by the platform the running binary was
built for: `sigildns_<version>_<goos>_<goarch>.zip`. An additional build (for
example arm64) must keep that name so its own build is offered it; an amd64
build is never offered the arm64 zip.

The release page also carries `SHA256SUMS.txt` (not inside the zip) so a
download can be verified:

```powershell
Get-FileHash .\sigildns_v0.3.0_windows_amd64.zip -Algorithm SHA256
```

Release builds use the `gui` tag (never `-tags tray`); the **tray icon is part of
the GUI build** and is off by default (Settings → Window & tray). In a `gui`
build the `--tray` command runs the native tray host without a window; the
`-tags tray` build remains a stub.

Both binaries embed `cmd/sigildns/sigildns.manifest`, which requests
`requireAdministrator`: Windows shows a UAC prompt on every launch and the
process runs elevated for its lifetime (a deliberate deviation from SPEC
§15.3.8).

The two binaries come from the same Go module. A single Windows exe cannot
cleanly be both console and GUI subsystem: a console-subsystem GUI leaves a
stray console window, and a `windowsgui` CLI loses stdout (breaking
`sigildns status`). The console binary keeps stdout; the GUI binary opens the
window when launched with no arguments.

## Cutting a release

The workflow lives at `.github/workflows/release.yml` and runs on
`windows-latest`.

1. Make sure the commit you are tagging is green (the `ci.yml` `build` matrix
   and `integration-windows` job). Tag pushes no longer start CI, so the release
   workflow checks the CI history for the tag's SHA itself and **refuses to
   publish** if the newest completed push run for that SHA failed or is still
   running.
2. Tag the release and push the tag:

   ```sh
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```

   Pushing a `v*` tag triggers the workflow. It can also be run manually from
   **Actions → release → Run workflow** (`workflow_dispatch`). Running it on a
   branch builds an artifact but does **not** create a GitHub Release; running
   it on a tag follows the tag path and does publish.

## What the workflow does

1. `actions/checkout` and `actions/setup-go` (both pinned to commit SHAs;
   `go-version-file: go.mod`).
2. **CI gate** — for tag builds, looks up the newest completed `ci.yml` **push**
   run for the tagged commit (pull-request runs are ignored). A successful run
   skips the duplicate `go vet` / `go test` steps (they already ran on the
   branch push). A failed or cancelled push run fails the release; a run that is
   still in progress also fails when no completed push run exists yet. Only when
   no push CI run exists at all (a tag on an unmerged commit) do the checks run
   here on Windows, so the Windows-only tests still execute. Manual
   (`workflow_dispatch`) runs on a branch always run them.
3. Builds **both** binaries:
   `GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X .../version.Version=<tag>" -o dist/sigildns.exe ./cmd/sigildns`
   and the same with `-tags gui` and `-H windowsgui` for
   `dist/sigildns-gui.exe`. `-s -w` matches `dev/build.sh` and strips the
   symbol table and DWARF to keep the download small; Go runtime stack traces
   still include function names and line numbers.
4. **Optional signing** — signs **both** binaries (see below).
5. Packages `dist/sigildns_<version>_windows_amd64.zip` containing both
   binaries, `config.example.yaml`, `README.md`, `SPEC.md`, `CHANGELOG.md`,
   `LICENSE`, `docs/`, and `installer/setup.ps1`.
6. Writes `dist/SHA256SUMS.txt` — the SHA-256 of the zip, published alongside
   it (the zip itself is never signed).
7. Verifies the package: extracts the zip, asserts every expected file is
   present (both binaries, config, README, SPEC, CHANGELOG, LICENSE, `docs/`,
   installer),
   recomputes the zip hash against `SHA256SUMS.txt`, and (when signing ran)
   requires a `Valid` Authenticode signature on each binary.
8. Uploads the zip and checksums as a build artifact.
9. For tag builds, publishes the GitHub Release **draft-first**:
   `gh release create --draft` → `gh release upload --clobber` → `gh release
   edit --draft=false`. A failed run therefore never leaves a public release
   without its assets, and re-running a tag build converges on the same
   draft.

Release runs are serialized per ref (`concurrency: release-${{ github.ref }}`),
so a re-run for the same tag queues instead of interleaving. Different tags run
independently.

## Release notes

Release notes come from, in order:

1. `release-notes/<tag>.md` when that file exists in the tagged commit (a
   curated override).
2. The matching `## [<version>]` section of `CHANGELOG.md`, used verbatim.
3. `gh release create --generate-notes` (merged PRs and first-time
   contributors) when neither exists.

Write the CHANGELOG entry for anything a user must act on — changed default
profiles, interception or NRPT behaviour, service identity, config schema.
Generated notes do not know which of those changed. A `release-notes/<tag>.md`
override is only needed when the release text should differ from the changelog
(for example, extra upgrade instructions).

## Code signing secrets

Signing is optional. It is enabled only when **both** of these repository secrets
are set, otherwise the workflow prints a `::warning::` and continues unsigned:

| Secret | Value |
|---|---|
| `WINDOWS_CERT_PFX_BASE64` | Base64 of the `.pfx` (certificate + private key), single line |
| `WINDOWS_CERT_PASSWORD` | Password for the `.pfx` |

Generate the base64 on Windows:

```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes('codesign.pfx')) | Set-Clipboard
```

On Linux/macOS:

```sh
base64 -w0 codesign.pfx        # GNU coreutils
base64 codesign.pfx | tr -d '\n'   # BSD/macOS
```

Set them with the `gh` CLI:

```sh
gh secret set WINDOWS_CERT_PFX_BASE64 < codesign.pfx.b64
gh secret set WINDOWS_CERT_PASSWORD
```

Use an OV code-signing certificate; EV is only needed for a kernel driver, which
this project does not ship. Never commit the `.pfx` or its password.

## What the signing step runs

Both release binaries are signed — `sigildns.exe` and `sigildns-gui.exe`. The
workflow locates `signtool.exe` under the Windows SDK and runs:

```powershell
signtool sign /fd SHA256 /td SHA256 /tr http://timestamp.digicert.com `
    /f <decoded-pfx> /p <password> dist\sigildns.exe dist\sigildns-gui.exe

signtool verify /pa dist\sigildns.exe
signtool verify /pa dist\sigildns-gui.exe
```

`/fd SHA256` sets the file digest, `/td SHA256` the timestamp digest, and `/tr`
uses an RFC 3161 timestamp server. The PFX is written to `%RUNNER_TEMP%` and
deleted in a `finally` block.

## Signing manually

If you need to sign outside CI:

```powershell
# From a Developer Command Prompt / with the Windows SDK on PATH:
signtool sign /fd SHA256 /td SHA256 /tr http://timestamp.digicert.com `
    /f codesign.pfx /p '<password>' .\sigildns.exe

signtool verify /pa .\sigildns.exe
```

Verify the signature and timestamp afterwards with:

```powershell
Get-AuthenticodeSignature .\sigildns.exe | Format-List Status, SignerCertificate, TimeStamperCertificate
```

## After publishing

The release zip is unsigned when the secrets are absent. Users installing an
unsigned build may see SmartScreen warnings; that is expected until signing is
configured. `SHA256SUMS.txt` catches a corrupted download, not a tampered one —
it is served from the same place as the zip, so only the Authenticode signature
on the binaries is real proof of origin.

Installation instructions are in the installer scripts and the user-facing docs.
