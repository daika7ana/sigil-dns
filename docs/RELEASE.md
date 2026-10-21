# Releasing Sigil DNS

This document is for maintainers. It describes how to cut a release, what the
`release` workflow does, and how to configure or bypass code signing.

The user-facing documentation lives elsewhere; this file only covers the release
process.

## What ships (v1)

The release zip is `sigildns_<version>_windows_amd64.zip` and contains:

- `sigildns.exe` — the console-subsystem binary: CLI, service host, and the
  headless `--apply` elevation helper.
- `sigildns-gui.exe` — the GUI-subsystem configuration client (Status +
  Settings), built `-tags gui -ldflags "-H windowsgui"`.
- `config.example.yaml`, `README.md`, `LICENSE`.
- `installer/install.ps1`, `installer/uninstall.ps1`.

The release page also carries `SHA256SUMS.txt` (not inside the zip) so a
download can be verified:

```powershell
Get-FileHash .\sigildns_v0.1.0_windows_amd64.zip -Algorithm SHA256
```

Release builds use the `gui` tag (never `-tags tray`); the **tray UI is deferred
to v1.1**.

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
   workflow checks the CI history for the tag's SHA itself.
2. Tag the release and push the tag:

   ```sh
   git tag v0.1.0
   git push origin v0.1.0
   ```

   Pushing a `v*` tag triggers the workflow. It can also be run manually from
   **Actions → release → Run workflow** (`workflow_dispatch`); a manual run
   builds an artifact but does **not** create a GitHub Release (there is no tag).

## What the workflow does

1. `actions/checkout@v7`, `actions/setup-go@v7` (`go-version-file: go.mod`).
2. **CI gate** — for tag builds, looks up the `ci.yml` runs for the tagged
   commit. A successful run skips the duplicate `go vet` / `go test` steps
   (they already ran on the branch push). With no green run — a tag on an
   unmerged commit, or a tag pushed before CI finished — the checks run here on
   Windows, so the Windows-only tests still execute. Manual (`workflow_dispatch`)
   runs always run them.
3. Builds **both** binaries:
   `GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/sigildns.exe ./cmd/sigildns`
   and
   `GOOS=windows GOARCH=amd64 go build -trimpath -tags gui -ldflags "-s -w -H windowsgui" -o dist/sigildns-gui.exe ./cmd/sigildns`.
4. **Optional signing** — signs **both** binaries (see below).
5. Packages `dist/sigildns_<version>_windows_amd64.zip` containing both
   binaries, `config.example.yaml`, `README.md`, `LICENSE`, and both installer
   scripts.
6. Writes `dist/SHA256SUMS.txt` — the SHA-256 of the zip, published alongside
   it (the zip itself is never signed).
7. Verifies the package: extracts the zip, asserts both binaries are present,
   and (when signing ran) requires a `Valid` Authenticode signature on each.
8. Uploads the zip and checksums as a build artifact.
9. For tag builds, publishes the GitHub Release **draft-first**:
   `gh release create --draft` → `gh release upload --clobber` → `gh release
   edit --draft=false`. A failed run therefore never leaves a public release
   without its assets, and re-running a tag build converges on the same
   draft.

Release runs are serialized per ref
(`concurrency: release-${{ github.ref }}`), so a manual dispatch cannot
interleave uploads with a tag push.

## Release notes

Release notes come from `release-notes/<tag>.md` when that file exists in the
tagged commit; otherwise the workflow falls back to
`gh release create --generate-notes` (merged PRs and first-time contributors).

Write curated notes for anything a user must act on — changed default
profiles, interception or NRPT behaviour, service identity, config schema.
Generated notes do not know which of those changed. No file is required for
routine patch releases.

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
