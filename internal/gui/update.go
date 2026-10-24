//go:build gui

package gui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/daika7ana/sigil-dns/internal/version"
)

const (
	updateCheckTimeout = 15 * time.Second
	// A 14 MB asset takes about two minutes at 1 Mbps.  A 10-minute cap leaves
	// slow links room to finish instead of failing on every retry.
	updateDownloadTimeout = 10 * time.Minute
	fallbackUpdateName    = "sigildns_update.zip"
	checksumAssetName     = "SHA256SUMS.txt"
	maxSumsBytes          = 64 << 10
)

// updateAPIURL is a var only so a test can point it at a local server.
var updateAPIURL = "https://api.github.com/repos/daika7ana/sigil-dns/releases/latest"

// maxUpdateBytes caps a download at 128 MB.  It is a var only so a test can
// shrink the cap; production never changes it.
var maxUpdateBytes int64 = 128 << 20

// errUpdateTooLarge rejects an asset before or during the stream.
var errUpdateTooLarge = errors.New("update is larger than the size limit")

// errUpdateChecksum rejects a download that cannot be verified against the
// release's checksum manifest.  It never echoes a URL or a digest.
var errUpdateChecksum = errors.New("update checksum verification failed")

// releaseAsset is the part of a GitHub release asset the checker uses.
type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// releaseInfo is the part of the latest-release response the checker uses.
type releaseInfo struct {
	Tag    string         `json:"tag_name"`
	Page   string         `json:"html_url"`
	Assets []releaseAsset `json:"assets"`
}

// fetchLatestRelease performs the update check.  It is a package-level var so a
// headless test can stub it (mirroring openURL/openPath) and never hit the
// network.
var fetchLatestRelease = fetchLatestReleaseDefault

// newUpdateRequest builds a GitHub GET and rejects a non-200 response.  accept
// is optional; every request carries the versioned User-Agent.  The caller
// closes the body.
func newUpdateRequest(ctx context.Context, rawURL, accept string) (*http.Response, error) {
	u, err := validateURL(rawURL)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SigilDNS/"+version.Version)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()

		return nil, fmt.Errorf("update request returned %s", resp.Status)
	}

	return resp, nil
}

// fetchLatestReleaseDefault asks GitHub for the latest release.  The caller's
// context carries the timeout and window lifetime.
func fetchLatestReleaseDefault(ctx context.Context) (releaseInfo, error) {
	resp, err := newUpdateRequest(ctx, updateAPIURL, "application/vnd.github+json")
	if err != nil {
		return releaseInfo{}, err
	}
	defer resp.Body.Close()

	var rel releaseInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return releaseInfo{}, err
	}

	return rel, nil
}

// updatePhase is the About page update row's state.  The zero value is the
// checking state, so a page that has not finished its first check reads
// correctly.
type updatePhase int

const (
	updateChecking updatePhase = iota
	updateAvailable
	updateLatest
	updateCurrent
	updateFailed
	updateDownloading
	updateDownloaded
	updateDownloadFailed
	updateNoAsset
)

// updateState is the whole update row: the check outcome plus the download
// overlay.  base is the check outcome a download started from, so a dev build
// never turns into "Update available" mid-download.
type updateState struct {
	phase updatePhase
	base  updatePhase
	tag   string
	page  string
	asset releaseAsset
	sums  releaseAsset
	path  string
}

// semver is the major/minor/patch triple plus the pre-release the checker
// compares.  A final release (pre == "") outranks the same triple with a
// pre-release.
type semver struct {
	major, minor, patch int
	pre                 string
}

// describeSuffix matches git describe's development-build tail: "-1-g9f31662",
// "-1-g9f31662-dirty", or "-dirty".  That is not a semver pre-release, so a
// build after a tag must not look older than the tag.
var describeSuffix = regexp.MustCompile(`(?:-\d+-g[0-9a-fA-F]+)?-dirty$|-\d+-g[0-9a-fA-F]+$`)

// parseVersion accepts v1.2.3 or 1.2.3, strips a git-describe development
// suffix, keeps a real pre-release so v1.2.4 outranks v1.2.4-rc.1, and ignores
// build metadata.  Anything else (including "dev") is not comparable.
func parseVersion(s string) (semver, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	s = describeSuffix.ReplaceAllString(s, "")
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}

	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}

	var v semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		switch i {
		case 0:
			v.major = n
		case 1:
			v.minor = n
		default:
			v.patch = n
		}
	}
	v.pre = pre

	return v, true
}

// newerRelease reports whether latest is a strictly newer release than
// current.  Unparsable values are never newer, so a dev build cannot be told
// an update exists.
func newerRelease(current, latest string) bool {
	cur, ok := parseVersion(current)
	if !ok {
		return false
	}
	lat, ok := parseVersion(latest)
	if !ok {
		return false
	}

	switch {
	case lat.major != cur.major:
		return lat.major > cur.major
	case lat.minor != cur.minor:
		return lat.minor > cur.minor
	case lat.patch != cur.patch:
		return lat.patch > cur.patch
	case lat.pre == cur.pre:
		return false
	case lat.pre == "":
		return true
	case cur.pre == "":
		return false
	default:
		// shortcut: pre-release identifiers are compared as strings, so rc.10
		// sorts below rc.2; use full semver if rc-to-rc ordering matters.
		return lat.pre > cur.pre
	}
}

// updatePlatform is the platform a release asset must match.  It is the
// platform this binary was built for, not the host's: an amd64 build running
// under arm64 emulation must keep fetching amd64.
type updatePlatform struct {
	os   string
	arch string
}

// runningPlatform is the compiled binary's platform.  The asset suffix in a
// release is sigildns_<tag>_<goos>_<goarch>.zip, so an arm64 build asks for
// arm64 even when both zips are published.
var runningPlatform = updatePlatform{os: runtime.GOOS, arch: runtime.GOARCH}

// pickReleaseAsset finds the zip built for plat in a release.
func pickReleaseAsset(assets []releaseAsset, plat updatePlatform) (releaseAsset, bool) {
	const prefix = "sigildns_"
	suffix := "_" + plat.os + "_" + plat.arch + ".zip"
	for _, a := range assets {
		if a.URL != "" && strings.HasPrefix(a.Name, prefix) &&
			strings.HasSuffix(a.Name, suffix) && len(a.Name) > len(prefix)+len(suffix) {
			return a, true
		}
	}

	return releaseAsset{}, false
}

// updateFromRelease folds a fetch result into the row's state.  Pure, so the
// copy, the dev-build rule, the platform match, and the version-first ordering
// are testable without a window.
func updateFromRelease(current string, rel releaseInfo, plat updatePlatform, err error) updateState {
	if err != nil {
		return updateState{phase: updateFailed}
	}

	st := updateState{phase: updateLatest, tag: rel.Tag, page: rel.Page}
	for _, a := range rel.Assets {
		if strings.EqualFold(a.Name, checksumAssetName) {
			st.sums = a
			break
		}
	}
	if _, ok := parseVersion(current); ok {
		if _, ok := parseVersion(rel.Tag); ok {
			// A current build needs no asset at all, even when the release has
			// no zip for its platform.
			if !newerRelease(current, rel.Tag) {
				st.phase = updateCurrent
				st.base = st.phase

				return st
			}
			st.phase = updateAvailable
		}
	}

	asset, ok := pickReleaseAsset(rel.Assets, plat)
	if !ok {
		return updateState{phase: updateNoAsset, tag: rel.Tag, page: rel.Page}
	}
	st.asset = asset
	st.base = st.phase

	return st
}

// safeUpdateName reduces an asset name to a plain .zip basename.  A separator,
// a NUL, or a non-zip suffix falls back to a fixed name, so a download can
// never land outside the chosen folder.
func safeUpdateName(name string) string {
	if name == "" || strings.ContainsRune(name, 0) || strings.ContainsAny(name, `/\`) {
		return fallbackUpdateName
	}
	if name == "." || name == ".." || !strings.HasSuffix(strings.ToLower(name), ".zip") {
		return fallbackUpdateName
	}

	return name
}

// downloadsFolder is a var so a test can point at a temp folder; production
// resolves the user's Downloads known folder.
var downloadsFolder = downloadsFolderDefault

// updateDir is the user's Downloads folder, created on demand, with the system
// temp folder as the fallback when it is unavailable.
func updateDir() string {
	if dir, err := downloadsFolder(); err == nil && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			return dir
		}
	}

	return os.TempDir()
}

// fetchChecksums fetches the release's SHA256SUMS.txt.  Package-level var so a
// headless test can stub it.
var fetchChecksums = fetchChecksumsDefault

// fetchChecksumsDefault returns lowercase filename -> lowercase hex digest.
func fetchChecksumsDefault(ctx context.Context, asset releaseAsset) (map[string]string, error) {
	resp, err := newUpdateRequest(ctx, asset.URL, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSumsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSumsBytes {
		return nil, errors.New("checksum manifest is too large")
	}

	return parseChecksums(body), nil
}

// parseChecksums reads "<sha256>  <name>" lines, tolerating CRLF and the
// sha256sum binary marker.  Malformed lines are ignored.
func parseChecksums(data []byte) map[string]string {
	sums := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		digest := strings.ToLower(fields[0])
		if len(digest) != 64 {
			continue
		}
		if _, err := hex.DecodeString(digest); err != nil {
			continue
		}
		name := strings.ToLower(strings.TrimPrefix(fields[1], "*"))
		if name != "" {
			sums[name] = digest
		}
	}

	return sums
}

// downloadUpdate saves the asset and returns its path.  Package-level var so a
// headless test can stub it (mirroring openURL).
var downloadUpdate = downloadUpdateDefault

// downloadUpdateDefault streams the asset to a .part file and renames it into
// place only after its SHA-256 matches the release manifest.  It never extracts
// or executes anything, and removes the partial file on every failure.
func downloadUpdateDefault(ctx context.Context, asset, sums releaseAsset) (string, error) {
	if asset.Size > maxUpdateBytes {
		return "", errUpdateTooLarge
	}

	// Fail closed: an unverifiable download is not saved.
	if sums.URL == "" {
		return "", errUpdateChecksum
	}
	want, err := fetchChecksums(ctx, sums)
	if err != nil {
		return "", errUpdateChecksum
	}
	digest, ok := want[strings.ToLower(asset.Name)]
	if !ok {
		return "", errUpdateChecksum
	}

	resp, err := newUpdateRequest(ctx, asset.URL, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.ContentLength > maxUpdateBytes {
		return "", errUpdateTooLarge
	}

	name := safeUpdateName(asset.Name)
	dir := updateDir()

	// A predictable name in a user-writable folder lets an unelevated process
	// pre-create a hardlink or symlink there and redirect this elevated write
	// onto another file.  CreateTemp picks a random name with O_EXCL, so the
	// open never follows an existing link.
	f, err := os.CreateTemp(dir, ".sigildns-*.part")
	if err != nil {
		return "", err
	}
	part := f.Name()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxUpdateBytes+1))
	if err == nil && n > maxUpdateBytes {
		err = errUpdateTooLarge
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(part)

		return "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		_ = os.Remove(part)

		return "", errUpdateChecksum
	}

	final := filepath.Join(dir, name)
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)

		return "", err
	}

	markDownloaded(final)

	return final, nil
}

// startUpdateCheck runs one check off the frame thread and posts the outcome
// back with QueueCommand.  The goroutine never touches App.
func startUpdateCheck(w *gogui.Window) {
	app := gogui.State[App](w)
	if app.updateChecking {
		return
	}
	app.updateChecking = true
	app.update = updateState{phase: updateChecking}
	winCtx := w.Ctx()

	w.InvalidateLayout()

	go func() {
		ctx, cancel := context.WithTimeout(winCtx, updateCheckTimeout)
		defer cancel()

		rel, err := fetchLatestRelease(ctx)

		w.QueueCommand(func(win *gogui.Window) {
			a := gogui.State[App](win)
			a.updateChecking = false
			a.update = updateFromRelease(version.Version, rel, runningPlatform, err)
			win.InvalidateLayout()
		})
	}()
}

// startUpdateDownload streams the selected asset off the frame thread.  The
// phase flips to downloading synchronously, so a double click cannot start a
// second transfer.
func startUpdateDownload(w *gogui.Window) {
	app := gogui.State[App](w)
	if app.update.phase == updateDownloading || app.update.asset.URL == "" {
		return
	}

	app.update.phase = updateDownloading
	asset, sums := app.update.asset, app.update.sums
	winCtx := w.Ctx()

	w.InvalidateLayout()

	go func() {
		ctx, cancel := context.WithTimeout(winCtx, updateDownloadTimeout)
		defer cancel()

		path, err := downloadUpdate(ctx, asset, sums)

		w.QueueCommand(func(win *gogui.Window) {
			a := gogui.State[App](win)
			if err != nil {
				a.update.phase = updateDownloadFailed
			} else {
				a.update.phase = updateDownloaded
				a.update.path = path
			}
			win.InvalidateLayout()
		})
	}()
}

// updateCard is the About page's notify-only update block.  Downloads are only
// ever saved: nothing is extracted or run.
func updateCard(t gogui.Theme, app *App) gogui.View {
	return card(t, "gui:card:update", "Updates", []gogui.View{
		updateRow(t, app),
		caption(t, "Downloads are saved, not installed automatically."),
	})
}

// updateRow keeps the status text, the release tag, and the one control for
// the current state on a single line.
func updateRow(t gogui.Theme, app *App) gogui.View {
	st := app.update
	row := []gogui.View{
		gogui.Text(gogui.TextCfg{
			ID:        "gui:about:update:status",
			Text:      updateStatusText(st),
			TextStyle: t.TextStyleBody,
			Sizing:    gogui.FitFit,
		}),
	}
	if st.tag != "" {
		row = append(row, gogui.Text(gogui.TextCfg{
			ID:        "gui:about:update:tag",
			Text:      st.tag,
			TextStyle: t.TextStyleSecondary,
			Sizing:    gogui.FitFit,
		}))
	}
	if b := updateButton(t, st); b != nil {
		row = append(row, b)
	}

	return gogui.Row(gogui.ContainerCfg{
		ID:         "gui:about:update:row",
		Sizing:     gogui.FillFit,
		Spacing:    gogui.SpacingMedium,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		VAlign:     gogui.VAlignMiddle,
		Content:    row,
	})
}

// updateStatusText is the one-line outcome for a state.
func updateStatusText(st updateState) string {
	switch st.phase {
	case updateAvailable:
		return "Update available"
	case updateLatest:
		return "Latest release"
	case updateCurrent:
		return "You're up to date"
	case updateFailed:
		return "Couldn't check for updates"
	case updateDownloading:
		// Keep the claim the check made: a dev build stays "Latest release".
		if st.base == updateLatest {
			return "Latest release"
		}

		return "Update available"
	case updateDownloaded:
		return "Saved"
	case updateDownloadFailed:
		return "Couldn't download the update"
	case updateNoAsset:
		return "No download for this build"
	default:
		return "Checking for updates…"
	}
}

// updateButton is the single control for a state, or nil when none applies.
func updateButton(t gogui.Theme, st updateState) gogui.View {
	switch st.phase {
	case updateAvailable, updateLatest:
		return gogui.Button(gogui.ButtonCfg{
			ID:      "gui:about:update:download",
			Variant: gogui.ButtonPrimary,
			Label:   "Download",
			Sizing:  gogui.FitFit,
			OnClick: func(ctx gogui.EventCtx) { startUpdateDownload(ctx.Window) },
		})
	case updateDownloading:
		return gogui.Button(gogui.ButtonCfg{
			ID:       "gui:about:update:download",
			Variant:  gogui.ButtonSecondary,
			Label:    "Downloading…",
			Sizing:   gogui.FitFit,
			Disabled: true,
		})
	case updateDownloaded:
		dir := filepath.Dir(st.path)

		return gogui.Button(gogui.ButtonCfg{
			ID:      "gui:about:update:folder",
			Variant: gogui.ButtonSecondary,
			Label:   "Open folder",
			Sizing:  gogui.FitFit,
			OnClick: func(gogui.EventCtx) { _ = openPath(dir) },
		})
	case updateFailed:
		return gogui.Button(gogui.ButtonCfg{
			ID:      "gui:about:update:retry",
			Variant: gogui.ButtonSecondary,
			Label:   "Retry",
			Sizing:  gogui.FitFit,
			OnClick: func(ctx gogui.EventCtx) { startUpdateCheck(ctx.Window) },
		})
	case updateDownloadFailed:
		return gogui.Button(gogui.ButtonCfg{
			ID:      "gui:about:update:retry",
			Variant: gogui.ButtonSecondary,
			Label:   "Retry",
			Sizing:  gogui.FitFit,
			OnClick: func(ctx gogui.EventCtx) { startUpdateDownload(ctx.Window) },
		})
	case updateNoAsset:
		if st.page != "" {
			return linkButton(t, "gui:about:update:release", "View release", st.page)
		}
	}

	return nil
}
