//go:build gui

package gui

import (
	"path/filepath"
	"testing"
)

// TestGUILockPath proves the lock lives beside gui.json, user-scope.
func TestGUILockPath(t *testing.T) {
	got := guiLockPath(filepath.Join("a", "b", "gui.json"))
	want := filepath.Join("a", "b", "gui.lock")
	if got != want {
		t.Fatalf("guiLockPath = %q, want %q", got, want)
	}
}

// TestAcquireInstanceRefusesSecond proves a second launch is refused (the
// cross-platform logic, so it is covered off-Windows).
func TestAcquireInstanceRefusesSecond(t *testing.T) {
	guiPath := filepath.Join(t.TempDir(), "gui.json")

	first, err := acquireInstance(guiPath)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if first == nil {
		t.Fatal("first acquire returned no lock")
	}
	t.Cleanup(func() { _ = first.Release() })

	second, err := acquireInstance(guiPath)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if second != nil {
		_ = second.Release()

		t.Fatal("second acquire succeeded; a second window would open")
	}
}

// TestAcquireInstanceAfterRelease proves the lock is reusable once released.
func TestAcquireInstanceAfterRelease(t *testing.T) {
	guiPath := filepath.Join(t.TempDir(), "gui.json")

	first, err := acquireInstance(guiPath)
	if err != nil || first == nil {
		t.Fatalf("first acquire: lock=%v err=%v", first, err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	again, err := acquireInstance(guiPath)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if again == nil {
		t.Fatal("re-acquire after release returned no lock")
	}
	_ = again.Release()
}

// TestAcquireInstanceCreatesStateDir proves a first launch works before
// gui.json exists: the user-scope directory must be created here, because the
// lock file is opened with O_CREATE and saveSettings (which also creates it)
// does not run until after the window opens.
func TestAcquireInstanceCreatesStateDir(t *testing.T) {
	guiPath := filepath.Join(t.TempDir(), "SigilDNS", "gui.json")

	lock, err := acquireInstance(guiPath)
	if err != nil {
		t.Fatalf("acquireInstance on missing dir: %v", err)
	}
	if lock == nil {
		t.Fatal("acquireInstance returned no lock and no error")
	}
	_ = lock.Release()
}
