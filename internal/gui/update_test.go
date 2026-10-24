//go:build gui

package gui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/daika7ana/sigil-dns/internal/version"
)

// TestParseVersion pins the tolerated tag shapes and the rejects.
func TestParseVersion(t *testing.T) {
	ok := map[string]semver{
		"v1.2.3":                  {major: 1, minor: 2, patch: 3},
		"1.2.3":                   {major: 1, minor: 2, patch: 3},
		" v1.2.3 ":                {major: 1, minor: 2, patch: 3},
		"v1.2.3-beta.1":           {major: 1, minor: 2, patch: 3, pre: "beta.1"},
		"1.2.3+build.5":           {major: 1, minor: 2, patch: 3},
		"v0.0.1-rc.1+abc":         {major: 0, minor: 0, patch: 1, pre: "rc.1"},
		"v0.4.0-1-g9f31662":       {major: 0, minor: 4, patch: 0},
		"v0.4.0-1-g9f31662-dirty": {major: 0, minor: 4, patch: 0},
		"v0.4.0-dirty":            {major: 0, minor: 4, patch: 0},
		"v1.2.3-rc.1-2-gabc":      {major: 1, minor: 2, patch: 3, pre: "rc.1"},
		"v1.2.3-rc.1-dirty":       {major: 1, minor: 2, patch: 3, pre: "rc.1"},
	}
	for in, want := range ok {
		got, parsed := parseVersion(in)
		if !parsed || got != want {
			t.Errorf("parseVersion(%q) = %v, %v; want %v, true", in, got, parsed, want)
		}
	}

	for _, in := range []string{"", "dev", "v1.2", "1", "v1.2.x", "v1.2.3.4", "v-1.2.3", "release-1.2.3"} {
		if _, parsed := parseVersion(in); parsed {
			t.Errorf("parseVersion(%q) parsed, want reject", in)
		}
	}
}

// TestNewerRelease pins the comparison and the dev/unparsable rule.
func TestNewerRelease(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v1.2.3", "v1.2.4", true},
		{"v1.2.3", "v1.3.0", true},
		{"v1.2.3", "v2.0.0", true},
		{"1.2.3", "1.2.4", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.4", "v1.2.3", false},
		{"v2.0.0", "v1.9.9", false},
		{"dev", "v9.9.9", false},
		{"", "v9.9.9", false},
		{"v1.2.3", "dev", false},
		{"v1.2.3", "garbage", false},
		{"v1.2.3-rc.1", "v1.2.3", true},
		{"v1.2.3", "v1.2.3-rc.1", false},
		{"v1.2.3-rc.1", "v1.2.3-rc.2", true},
		{"v1.2.3-rc.2", "v1.2.3-rc.1", false},
		{"v1.2.3-rc.1", "v1.2.3-rc.1", false},
		{"v0.4.0-1-g9f31662", "v0.4.0", false},
		{"v0.4.0-1-g9f31662-dirty", "v0.4.0", false},
		{"v0.4.0-dirty", "v0.4.0", false},
		{"v0.4.0-1-g9f31662", "v0.4.1", true},
	}
	for _, tc := range cases {
		if got := newerRelease(tc.current, tc.latest); got != tc.want {
			t.Errorf("newerRelease(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

// TestPickReleaseAsset proves the asset must match the running binary's own
// platform, so an arm64 build picks arm64 and never falls back to amd64.
func TestPickReleaseAsset(t *testing.T) {
	assets := []releaseAsset{
		{Name: "sigildns_v1.2.4_windows_arm64.zip", URL: "https://example.test/arm64"},
		{Name: "sigildns_v1.2.4_linux_amd64.tar.gz", URL: "https://example.test/linux"},
		{Name: "sigildns-v1.2.4-windows_amd64.zip", URL: "https://example.test/dash"},
		{Name: "sigildns__windows_amd64.zip", URL: "https://example.test/empty"},
		{Name: "sigildns_v1.2.4_windows_amd64.zip", URL: "https://example.test/amd64"},
	}

	for _, tc := range []struct {
		name string
		plat updatePlatform
		want string
	}{
		{"amd64", updatePlatform{os: "windows", arch: "amd64"}, "https://example.test/amd64"},
		{"arm64", updatePlatform{os: "windows", arch: "arm64"}, "https://example.test/arm64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pickReleaseAsset(assets, tc.plat)
			if !ok || got.URL != tc.want {
				t.Fatalf("pickReleaseAsset = %+v, %v; want %s", got, ok, tc.want)
			}
		})
	}

	amdOnly := []releaseAsset{assets[4]}
	if _, ok := pickReleaseAsset(amdOnly, updatePlatform{os: "windows", arch: "arm64"}); ok {
		t.Error("arm64 build matched the amd64 zip")
	}
	if _, ok := pickReleaseAsset(assets[:4], updatePlatform{os: "windows", arch: "amd64"}); ok {
		t.Error("pickReleaseAsset matched a non-target asset")
	}
	if _, ok := pickReleaseAsset(nil, updatePlatform{os: "windows", arch: "amd64"}); ok {
		t.Error("pickReleaseAsset matched an empty list")
	}
}

// TestSafeUpdateName proves a hostile or non-zip name becomes the fallback.
func TestSafeUpdateName(t *testing.T) {
	cases := map[string]string{
		"sigildns_v1.2.3_windows_amd64.zip": "sigildns_v1.2.3_windows_amd64.zip",
		"sigildns_update.ZIP":               "sigildns_update.ZIP",
		"":                                  fallbackUpdateName,
		"../../evil.zip":                    fallbackUpdateName,
		`..\evil.zip`:                       fallbackUpdateName,
		"C:\\tmp\\evil.zip":                 fallbackUpdateName,
		"notes.txt":                         fallbackUpdateName,
		"evil.zip\x00":                      fallbackUpdateName,
		"..":                                fallbackUpdateName,
	}
	for in, want := range cases {
		if got := safeUpdateName(in); got != want {
			t.Errorf("safeUpdateName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestUpdateFromRelease pins the state mapping, including the dev-build rule
// and the platform match.
func TestUpdateFromRelease(t *testing.T) {
	assets := []releaseAsset{{Name: "sigildns_v1.2.4_windows_amd64.zip", URL: "https://example.test/a.zip"}}
	withAsset := func(tag string) releaseInfo {
		return releaseInfo{Tag: tag, Page: appRepoURL, Assets: assets}
	}
	winAmd64 := updatePlatform{os: "windows", arch: "amd64"}
	winArm64 := updatePlatform{os: "windows", arch: "arm64"}

	cases := []struct {
		name    string
		current string
		rel     releaseInfo
		plat    updatePlatform
		err     error
		want    updatePhase
	}{
		{"failed", "v1.2.3", releaseInfo{}, winAmd64, errors.New("offline"), updateFailed},
		{"no asset", "v1.2.3", releaseInfo{Tag: "v1.2.4", Page: appRepoURL}, winAmd64, nil, updateNoAsset},
		{"other platform", "v1.2.3", withAsset("v1.2.4"), winArm64, nil, updateNoAsset},
		{"same, other platform", "v1.2.4", withAsset("v1.2.4"), winArm64, nil, updateCurrent},
		{"newer", "v1.2.3", withAsset("v1.2.4"), winAmd64, nil, updateAvailable},
		{"rc to final", "v1.2.4-rc.1", withAsset("v1.2.4"), winAmd64, nil, updateAvailable},
		{"post-tag dev", "v1.2.4-1-g9f31662-dirty", withAsset("v1.2.4"), winAmd64, nil, updateCurrent},
		{"same", "v1.2.4", withAsset("v1.2.4"), winAmd64, nil, updateCurrent},
		{"older", "v1.3.0", withAsset("v1.2.4"), winAmd64, nil, updateCurrent},
		{"dev", "dev", withAsset("v9.9.9"), winAmd64, nil, updateLatest},
		{"unparsable current", "nightly", withAsset("v9.9.9"), winAmd64, nil, updateLatest},
		{"unparsable latest", "v1.2.3", withAsset("nightly"), winAmd64, nil, updateLatest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := updateFromRelease(tc.current, tc.rel, tc.plat, tc.err)
			if got.phase != tc.want {
				t.Fatalf("phase = %v, want %v", got.phase, tc.want)
			}
			if got.phase == updateLatest || got.phase == updateAvailable || got.phase == updateCurrent {
				if got.tag != tc.rel.Tag {
					t.Errorf("tag = %q, want %q", got.tag, tc.rel.Tag)
				}
			}
			if got.phase == updateNoAsset && got.page != appRepoURL {
				t.Errorf("page = %q, want %q", got.page, appRepoURL)
			}
		})
	}
}

// TestAboutUpdateStates renders every row state and checks the status text,
// the tag, and the one control that applies.
func TestAboutUpdateStates(t *testing.T) {
	asset := releaseAsset{Name: "sigildns_v1.2.4_windows_amd64.zip", URL: "https://example.test/a.zip"}
	cases := []struct {
		name   string
		st     updateState
		status string
		button string
		label  string
	}{
		{"checking", updateState{phase: updateChecking}, "Checking for updates…", "", ""},
		{
			"available",
			updateState{phase: updateAvailable, base: updateAvailable, tag: "v1.2.4", asset: asset},
			"Update available", "gui:about:update:download", "Download",
		},
		{
			"dev latest",
			updateState{phase: updateLatest, base: updateLatest, tag: "v1.2.4", asset: asset},
			"Latest release", "gui:about:update:download", "Download",
		},
		{
			"current",
			updateState{phase: updateCurrent, base: updateCurrent, tag: "v1.2.4", asset: asset},
			"You're up to date", "", "",
		},
		{
			"check failed",
			updateState{phase: updateFailed},
			"Couldn't check for updates", "gui:about:update:retry", "Retry",
		},
		{
			"downloading",
			updateState{phase: updateDownloading, base: updateLatest, tag: "v1.2.4", asset: asset},
			"Latest release", "gui:about:update:download", "Downloading…",
		},
		{
			"downloaded",
			updateState{phase: updateDownloaded, base: updateAvailable, tag: "v1.2.4", asset: asset,
				path: filepath.Join("home", "Downloads", asset.Name)},
			"Saved", "gui:about:update:folder", "Open folder",
		},
		{
			"download failed",
			updateState{phase: updateDownloadFailed, base: updateAvailable, tag: "v1.2.4", asset: asset},
			"Couldn't download the update", "gui:about:update:retry", "Retry",
		},
		{
			"no asset",
			updateState{phase: updateNoAsset, tag: "v1.2.4", page: appRepoURL},
			"No download for this build", "gui:about:update:release", "View release",
		},
	}

	buttons := []string{
		"gui:about:update:download",
		"gui:about:update:retry",
		"gui:about:update:folder",
		"gui:about:update:release",
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
			app.page = pageAbout
			app.update = tc.st

			w := gogui.NewTestWindow(t, gogui.WindowCfg{
				Width: defaultWidth, Height: defaultHeight, State: app,
			})
			w.SetView(view)
			root := w.TestRender(nil)

			texts := textSet(root)
			if !texts[tc.status] {
				t.Errorf("status %q not rendered", tc.status)
			}
			if tc.st.tag != "" && !texts[tc.st.tag] {
				t.Errorf("tag %q not rendered", tc.st.tag)
			}
			if tc.label != "" && !texts[tc.label] {
				t.Errorf("button label %q not rendered", tc.label)
			}

			for _, id := range buttons {
				want := 0
				if id == tc.button {
					want = 1
				}
				if got := len(w.ResolveID(id)); got != want {
					t.Errorf("%s rendered %d times, want %d", id, got, want)
				}
			}
		})
	}
}

// TestAboutOpenChecksOnce proves opening About starts exactly one check per
// run, and revisiting does not fetch again.
func TestAboutOpenChecksOnce(t *testing.T) {
	origFetch, origVersion := fetchLatestRelease, version.Version
	defer func() { fetchLatestRelease, version.Version = origFetch, origVersion }()
	usePlatform(t, "windows", "amd64")

	version.Version = "v1.0.0"
	calls := 0
	fetchLatestRelease = func(context.Context) (releaseInfo, error) {
		calls++

		return releaseInfo{
			Tag:  "v1.1.0",
			Page: appRepoURL,
			Assets: []releaseAsset{{
				Name: "sigildns_v1.1.0_windows_amd64.zip",
				URL:  "https://example.test/a.zip",
			}},
		}, nil
	}

	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	if err := w.TestClick("gui:nav:about"); err != nil {
		t.Fatalf("open About: %v", err)
	}
	waitUpdatePhase(t, w, app, updateAvailable)

	if calls != 1 {
		t.Fatalf("checks = %d, want 1", calls)
	}

	// Revisiting About must not start another check.
	if err := w.TestClick("gui:nav:about"); err != nil {
		t.Fatalf("revisit About: %v", err)
	}
	for i := 0; i < 5; i++ {
		w.FrameFn()
	}
	if calls != 1 {
		t.Errorf("checks after revisit = %d, want 1", calls)
	}
}

// TestAboutDevNeverClaimsUpdate proves a dev build shows the latest release
// with a Download button and never "Update available".
func TestAboutDevNeverClaimsUpdate(t *testing.T) {
	origFetch, origVersion := fetchLatestRelease, version.Version
	defer func() { fetchLatestRelease, version.Version = origFetch, origVersion }()
	usePlatform(t, "windows", "amd64")

	version.Version = "dev"
	fetchLatestRelease = func(context.Context) (releaseInfo, error) {
		return releaseInfo{
			Tag:  "v9.9.9",
			Page: appRepoURL,
			Assets: []releaseAsset{{
				Name: "sigildns_v9.9.9_windows_amd64.zip",
				URL:  "https://example.test/a.zip",
			}},
		}, nil
	}

	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	if err := w.TestClick("gui:nav:about"); err != nil {
		t.Fatalf("open About: %v", err)
	}
	waitUpdatePhase(t, w, app, updateLatest)

	root := w.TestRender(nil)
	texts := textSet(root)
	if !texts["Latest release"] {
		t.Error("dev build does not show the latest release")
	}
	if texts["Update available"] {
		t.Error("dev build claims an update is available")
	}
	if len(w.ResolveID("gui:about:update:download")) != 1 {
		t.Error("dev build does not offer Download")
	}
}

// TestAboutCheckFailureRetry proves a failed check shows the error state with
// Retry, and Retry checks again.
func TestAboutCheckFailureRetry(t *testing.T) {
	origFetch, origVersion := fetchLatestRelease, version.Version
	defer func() { fetchLatestRelease, version.Version = origFetch, origVersion }()
	usePlatform(t, "windows", "amd64")

	version.Version = "dev"
	calls := 0
	fail := true
	fetchLatestRelease = func(context.Context) (releaseInfo, error) {
		calls++
		if fail {
			return releaseInfo{}, errors.New("offline")
		}

		return releaseInfo{
			Tag:  "v1.2.3",
			Page: appRepoURL,
			Assets: []releaseAsset{{
				Name: "sigildns_v1.2.3_windows_amd64.zip",
				URL:  "https://example.test/a.zip",
			}},
		}, nil
	}

	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	if err := w.TestClick("gui:nav:about"); err != nil {
		t.Fatalf("open About: %v", err)
	}
	waitUpdatePhase(t, w, app, updateFailed)

	root := w.TestRender(nil)
	if !textSet(root)["Couldn't check for updates"] {
		t.Error("failure status not rendered")
	}
	if len(w.ResolveID("gui:about:update:retry")) != 1 {
		t.Fatal("failure state does not offer Retry")
	}

	fail = false
	if err := w.TestClick("gui:about:update:retry"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	waitUpdatePhase(t, w, app, updateLatest)

	if calls != 2 {
		t.Errorf("checks = %d, want 2", calls)
	}
}

// TestAboutCheckSingleFlight proves a second start while a check is in flight
// is a no-op, so a double-clicked Retry cannot fetch twice.
func TestAboutCheckSingleFlight(t *testing.T) {
	origFetch, origVersion := fetchLatestRelease, version.Version
	defer func() { fetchLatestRelease, version.Version = origFetch, origVersion }()
	usePlatform(t, "windows", "amd64")
	version.Version = "v1.0.0"

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	calls := 0
	fetchLatestRelease = func(context.Context) (releaseInfo, error) {
		calls++
		entered <- struct{}{}
		<-release

		return releaseInfo{
			Tag:  "v1.1.0",
			Page: appRepoURL,
			Assets: []releaseAsset{{
				Name: "sigildns_v1.1.0_windows_amd64.zip",
				URL:  "https://example.test/a.zip",
			}},
		}, nil
	}

	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	startUpdateCheck(w)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("check did not start")
	}

	startUpdateCheck(w) // in flight: must be a no-op
	close(release)
	waitUpdatePhase(t, w, app, updateAvailable)

	if calls != 1 {
		t.Fatalf("checks = %d, want 1", calls)
	}
}

// TestAboutDownloadFlow proves the Download click starts one transfer, the
// saved state offers Open folder, and openPath runs only on that click.
func TestAboutDownloadFlow(t *testing.T) {
	origDownload := downloadUpdate
	defer func() { downloadUpdate = origDownload }()

	dir := filepath.Join("home", "Downloads")
	saved := filepath.Join(dir, "sigildns_v1.2.4_windows_amd64.zip")
	calls := 0
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	downloadUpdate = func(context.Context, releaseAsset, releaseAsset) (string, error) {
		calls++
		entered <- struct{}{}
		<-release

		return saved, nil
	}

	var opened string
	origOpen := openPath
	openPath = func(p string) error {
		opened = p

		return nil
	}
	defer func() { openPath = origOpen }()

	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.page = pageAbout
	app.update = updateState{
		phase: updateAvailable,
		base:  updateAvailable,
		tag:   "v1.2.4",
		asset: releaseAsset{Name: "sigildns_v1.2.4_windows_amd64.zip", URL: "https://example.test/a.zip"},
	}

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	// The phase flips synchronously, so a second start while downloading is a
	// no-op.
	if err := w.TestClick("gui:about:update:download"); err != nil {
		t.Fatalf("download: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("download did not start")
	}
	if app.update.phase != updateDownloading {
		t.Fatalf("phase = %v, want downloading", app.update.phase)
	}
	startUpdateDownload(w)
	if calls != 1 {
		t.Fatalf("downloads while in flight = %d, want 1", calls)
	}
	close(release)
	waitUpdatePhase(t, w, app, updateDownloaded)

	if calls != 1 {
		t.Fatalf("downloads = %d, want 1", calls)
	}
	if opened != "" {
		t.Errorf("openPath ran during download: %q", opened)
	}

	root := w.TestRender(nil)
	if !textSet(root)["Saved"] {
		t.Error("saved state not rendered")
	}
	if err := w.TestClick("gui:about:update:folder"); err != nil {
		t.Fatalf("open folder: %v", err)
	}
	if opened != dir {
		t.Errorf("openPath(%q), want %q", opened, dir)
	}
}

// TestAboutDownloadFailureRetry proves a failed download shows its failure and
// Retry starts another transfer.
func TestAboutDownloadFailureRetry(t *testing.T) {
	origDownload := downloadUpdate
	defer func() { downloadUpdate = origDownload }()

	calls := 0
	downloadUpdate = func(context.Context, releaseAsset, releaseAsset) (string, error) {
		calls++

		return "", errors.New("network down")
	}

	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.page = pageAbout
	app.update = updateState{
		phase: updateAvailable,
		base:  updateAvailable,
		asset: releaseAsset{Name: "sigildns_v1.2.4_windows_amd64.zip", URL: "https://example.test/a.zip"},
	}

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	if err := w.TestClick("gui:about:update:download"); err != nil {
		t.Fatalf("download: %v", err)
	}
	waitUpdatePhase(t, w, app, updateDownloadFailed)

	root := w.TestRender(nil)
	if !textSet(root)["Couldn't download the update"] {
		t.Error("download failure status not rendered")
	}
	if err := w.TestClick("gui:about:update:retry"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	waitUpdatePhase(t, w, app, updateDownloadFailed)

	if calls != 2 {
		t.Errorf("downloads = %d, want 2", calls)
	}
}

// TestDownloadUpdateSavesUnderDownloads proves a successful download lands in
// Downloads with no .part left behind.
func TestDownloadUpdateSavesUnderDownloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	useTempDownloads(t, home)

	payload := []byte("PK zip bytes")
	name := "sigildns_v1.2.3_windows_amd64.zip"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "SigilDNS/") {
			t.Errorf("User-Agent = %q, want SigilDNS/<version>", ua)
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	asset := releaseAsset{
		Name: name,
		URL:  srv.URL + "/asset",
		Size: int64(len(payload)),
	}
	path, err := downloadUpdateDefault(context.Background(), asset, checksumServer(t, name, payload))
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	want := filepath.Join(home, "Downloads", asset.Name)
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("saved bytes = %q, want %q", got, payload)
	}
	if parts, _ := filepath.Glob(filepath.Join(home, "Downloads", "*.part")); len(parts) != 0 {
		t.Errorf("partial files left: %v", parts)
	}
}

// TestDownloadUpdateSanitisesName proves hostile asset names stay inside the
// chosen folder under the fallback name.
func TestDownloadUpdateSanitisesName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "PK")
	}))
	defer srv.Close()

	for _, name := range []string{"../../evil.zip", `..\evil.zip`, "notes.txt"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			useTempDownloads(t, home)

			payload := []byte("PK")
			path, err := downloadUpdateDefault(context.Background(),
				releaseAsset{Name: name, URL: srv.URL}, checksumServer(t, name, payload))
			if err != nil {
				t.Fatalf("download: %v", err)
			}

			want := filepath.Join(home, "Downloads", fallbackUpdateName)
			if path != want {
				t.Errorf("path = %q, want %q", path, want)
			}
			if filepath.Dir(path) != filepath.Join(home, "Downloads") {
				t.Errorf("saved outside Downloads: %q", path)
			}
		})
	}
}

// TestDownloadUpdateRejectsOversize proves the streaming cap stops a body with
// no Content-Length and removes the partial file.
func TestDownloadUpdateRejectsOversize(t *testing.T) {
	origCap := maxUpdateBytes
	maxUpdateBytes = 16
	defer func() { maxUpdateBytes = origCap }()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	useTempDownloads(t, home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // force chunked: no Content-Length to pre-check
		_, _ = w.Write(bytes.Repeat([]byte("x"), 17))
	}))
	defer srv.Close()

	name := "sigildns_v1.2.3_windows_amd64.zip"
	_, err := downloadUpdateDefault(context.Background(), releaseAsset{
		Name: name,
		URL:  srv.URL,
	}, checksumServer(t, name, bytes.Repeat([]byte("x"), 17)))
	if !errors.Is(err, errUpdateTooLarge) {
		t.Fatalf("err = %v, want errUpdateTooLarge", err)
	}
	if parts, _ := filepath.Glob(filepath.Join(home, "Downloads", "*")); len(parts) != 0 {
		t.Errorf("files left after oversize download: %v", parts)
	}
}

// TestDownloadUpdateRejectsBadStatus proves a non-200 leaves no file.
func TestDownloadUpdateRejectsBadStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	useTempDownloads(t, home)

	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	name := "sigildns_v1.2.3_windows_amd64.zip"
	_, err := downloadUpdateDefault(context.Background(), releaseAsset{
		Name: name,
		URL:  srv.URL,
	}, checksumServer(t, name, []byte("PK")))
	if err == nil {
		t.Fatal("download succeeded on 404")
	}
	if files, _ := filepath.Glob(filepath.Join(home, "Downloads", "*")); len(files) != 0 {
		t.Errorf("files left after failed download: %v", files)
	}
}

// TestDownloadUpdateFallsBackToTemp proves an unusable home folder falls back
// to the system temp folder.
func TestDownloadUpdateFallsBackToTemp(t *testing.T) {
	homeFile := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(homeFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	for _, env := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(env, homeFile)
	}
	for _, env := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(env, tmp)
	}
	useTempDownloads(t, homeFile)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "PK")
	}))
	defer srv.Close()

	asset := releaseAsset{Name: "sigildns_v1.2.3_windows_amd64.zip", URL: srv.URL}
	path, err := downloadUpdateDefault(context.Background(), asset, checksumServer(t, asset.Name, []byte("PK")))
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	want := filepath.Join(tmp, asset.Name)
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

// TestFetchLatestReleaseDefault pins the API path, both required headers, and
// the decoded fields, so a wrong URL or header cannot ship untested.
func TestFetchLatestReleaseDefault(t *testing.T) {
	orig := updateAPIURL
	defer func() { updateAPIURL = orig }()

	var gotPath, gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		_, _ = io.WriteString(w, `{"tag_name":"v1.2.4","html_url":"https://example.test/release",`+
			`"assets":[{"name":"sigildns_v1.2.4_windows_amd64.zip",`+
			`"browser_download_url":"https://example.test/a.zip","size":42}]}`)
	}))
	defer srv.Close()

	updateAPIURL = srv.URL + "/repos/daika7ana/sigil-dns/releases/latest"
	rel, err := fetchLatestReleaseDefault(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	if gotPath != "/repos/daika7ana/sigil-dns/releases/latest" {
		t.Errorf("path = %q", gotPath)
	}
	if gotUA != "SigilDNS/"+version.Version {
		t.Errorf("User-Agent = %q, want SigilDNS/%s", gotUA, version.Version)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if rel.Tag != "v1.2.4" || rel.Page != "https://example.test/release" {
		t.Errorf("release = %+v", rel)
	}
	if len(rel.Assets) != 1 || rel.Assets[0].Name != "sigildns_v1.2.4_windows_amd64.zip" ||
		rel.Assets[0].URL != "https://example.test/a.zip" || rel.Assets[0].Size != 42 {
		t.Errorf("assets = %+v", rel.Assets)
	}

	bad := httptest.NewServer(http.NotFoundHandler())
	defer bad.Close()
	updateAPIURL = bad.URL
	if _, err := fetchLatestReleaseDefault(context.Background()); err == nil {
		t.Error("a non-200 response did not fail")
	}
}

// TestDownloadUpdateIgnoresPreparedPartName proves a hardlink planted at the old
// fixed .part name cannot redirect the elevated write onto another file.
func TestDownloadUpdateIgnoresPreparedPartName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	useTempDownloads(t, home)

	downloads := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatal(err)
	}
	asset := releaseAsset{Name: "sigildns_v1.2.3_windows_amd64.zip"}
	canary := filepath.Join(home, "canary.txt")
	if err := os.WriteFile(canary, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(downloads, asset.Name+".part")
	if err := os.Link(canary, planted); err != nil {
		t.Skipf("hardlinks unsupported here: %v", err)
	}

	payload := []byte("PK zip bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	asset.URL = srv.URL

	path, err := downloadUpdateDefault(context.Background(), asset, checksumServer(t, asset.Name, payload))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if path != filepath.Join(downloads, asset.Name) {
		t.Errorf("path = %q", path)
	}

	got, err := os.ReadFile(canary)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep me" {
		t.Errorf("planted link was written through: canary = %q", got)
	}
}

// TestDownloadUpdateChecksum fails closed without a usable manifest entry.
func TestDownloadUpdateChecksum(t *testing.T) {
	payload := []byte("PK zip bytes")
	name := "sigildns_v1.2.3_windows_amd64.zip"
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	cases := []struct {
		name     string
		manifest string
		withSums bool
	}{
		{"no manifest", "", false},
		{"entry missing", digest + "  other.zip\n", true},
		{"digest mismatch", strings.Repeat("0", 64) + "  " + name + "\n", true},
		{"malformed", "not a checksum\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			useTempDownloads(t, home)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(payload)
			}))
			t.Cleanup(srv.Close)

			sums := releaseAsset{}
			if tc.withSums {
				body := tc.manifest
				sumsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, body)
				}))
				t.Cleanup(sumsSrv.Close)
				sums = releaseAsset{Name: checksumAssetName, URL: sumsSrv.URL}
			}

			_, err := downloadUpdateDefault(context.Background(),
				releaseAsset{Name: name, URL: srv.URL}, sums)
			if !errors.Is(err, errUpdateChecksum) {
				t.Fatalf("err = %v, want errUpdateChecksum", err)
			}
			if files, _ := filepath.Glob(filepath.Join(home, "Downloads", "*")); len(files) != 0 {
				t.Errorf("files left after failed download: %v", files)
			}
		})
	}
}

// TestParseChecksums pins CRLF, the binary marker, and malformed-line skipping.
func TestParseChecksums(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("B", 64)
	sums := parseChecksums([]byte(a + "  good.zip\r\n" + b + " *binary.zip\n" +
		"short  bad.zip\n" + strings.Repeat("z", 64) + "  nonhex.zip\n"))

	if sums["good.zip"] != a || sums["binary.zip"] != strings.ToLower(b) {
		t.Fatalf("sums = %v", sums)
	}
	for _, name := range []string{"bad.zip", "nonhex.zip"} {
		if _, ok := sums[name]; ok {
			t.Errorf("malformed line for %q parsed", name)
		}
	}
}

// checksumServer serves a SHA256SUMS.txt entry for one asset.
func checksumServer(t *testing.T, name string, payload []byte) releaseAsset {
	t.Helper()

	sum := sha256.Sum256(payload)
	body := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return releaseAsset{Name: checksumAssetName, URL: srv.URL}
}

// useTempDownloads points the download folder at a test temp home, so the
// platform known-folder lookup is never used in tests.
func useTempDownloads(t *testing.T, home string) {
	t.Helper()

	orig := downloadsFolder
	downloadsFolder = func() (string, error) { return filepath.Join(home, "Downloads"), nil }
	t.Cleanup(func() { downloadsFolder = orig })
}

// usePlatform pins the compiled platform for a test that drives the real check,
// so the fixtures are not tied to the host running the tests.
func usePlatform(t *testing.T, goos, goarch string) {
	t.Helper()

	orig := runningPlatform
	runningPlatform = updatePlatform{os: goos, arch: goarch}
	t.Cleanup(func() { runningPlatform = orig })
}

// waitUpdatePhase drives frames until the update row reaches want.
func waitUpdatePhase(t *testing.T, w *gogui.Window, app *App, want updatePhase) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.FrameFn()
		if app.update.phase == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}

	t.Fatalf("update phase = %v, want %v", app.update.phase, want)
}
