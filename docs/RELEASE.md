# Releasing dns-client

This document is for maintainers. It describes how to cut a release, what the
`release` workflow does, and how to configure or bypass code signing.

The user-facing documentation lives elsewhere; this file only covers the release
process.

## What ships (v1)

The release zip is `dns-client_<version>_windows_amd64.zip` and contains:

- `dns-client.exe` — the console-subsystem binary: CLI, service host, and the
  headless `--apply` elevation helper.
- `dns-client-gui.exe` — the GUI-subsystem configuration client (Status +
  Settings), built `-tags gui -ldflags "-H windowsgui"`.
- `config.example.yaml`, `README.md`, `LICENSE`.
- `installer/install.ps1`, `installer/uninstall.ps1`.

Release builds use the `gui` tag (never `-tags tray`); the **tray UI is deferred
to v1.1**.

Both binaries embed `cmd/dns-client/dns-client.manifest`, which requests
`requireAdministrator`: Windows shows a UAC prompt on every launch and the
process runs elevated for its lifetime (a deliberate deviation from SPEC
§15.3.8).

The two binaries come from the same Go module. A single Windows exe cannot
cleanly be both console and GUI subsystem: a console-subsystem GUI leaves a
stray console window, and a `windowsgui` CLI loses stdout (breaking
`dns-client status`). The console binary keeps stdout; the GUI binary opens the
window when launched with no arguments.

## Cutting a release

The workflow lives at `.github/workflows/release.yml` and runs on
`windows-latest`.

1. Make sure `main` is green (the `ci.yml` `build` matrix and
   `integration-windows` job).
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
2. `go vet ./...` and `go test ./...` (on Windows, so the Windows-only tests run).
3. Builds **both** binaries:
   `GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/dns-client.exe ./cmd/dns-client`
   and
   `GOOS=windows GOARCH=amd64 go build -trimpath -tags gui -ldflags "-s -w -H windowsgui" -o dist/dns-client-gui.exe ./cmd/dns-client`.
4. **Optional signing** — signs **both** binaries (see below).
5. Packages `dist/dns-client_<version>_windows_amd64.zip` containing both
   binaries, `config.example.yaml`, `README.md`, `LICENSE`, and both installer
   scripts.
6. Uploads the zip as a build artifact, and for tag builds creates (or updates)
   the GitHub Release and attaches the zip.

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

Both release binaries are signed — `dns-client.exe` and `dns-client-gui.exe`. The
workflow locates `signtool.exe` under the Windows SDK and runs:

```powershell
signtool sign /fd SHA256 /td SHA256 /tr http://timestamp.digicert.com `
    /f <decoded-pfx> /p <password> dist\dns-client.exe dist\dns-client-gui.exe

signtool verify /pa dist\dns-client.exe
signtool verify /pa dist\dns-client-gui.exe
```

`/fd SHA256` sets the file digest, `/td SHA256` the timestamp digest, and `/tr`
uses an RFC 3161 timestamp server. The PFX is written to `%RUNNER_TEMP%` and
deleted in a `finally` block.

## Signing manually

If you need to sign outside CI:

```powershell
# From a Developer Command Prompt / with the Windows SDK on PATH:
signtool sign /fd SHA256 /td SHA256 /tr http://timestamp.digicert.com `
    /f codesign.pfx /p '<password>' .\dns-client.exe

signtool verify /pa .\dns-client.exe
```

Verify the signature and timestamp afterwards with:

```powershell
Get-AuthenticodeSignature .\dns-client.exe | Format-List Status, SignerCertificate, TimeStamperCertificate
```

## After publishing

The release zip is unsigned when the secrets are absent. Users installing an
unsigned build may see SmartScreen warnings; that is expected until signing is
configured. Installation instructions are in the installer scripts and the
user-facing docs.
