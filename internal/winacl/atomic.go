package winacl

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteFileAtomic writes data to path via a temp file in the same directory,
// fsyncs it, restricts it to SYSTEM + Administrators, renames it into place and
// fsyncs the directory.  Securing the directory before the temp file exists and
// the temp file before the rename keeps an ACL failure non-destructive: a failed
// save leaves the previous file untouched.  Shared by the config and state
// writers.
func WriteFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating dir: %w", err)
	}

	// Restrict the directory before the temp file is created in it.
	if err = SecureDir(dir); err != nil {
		return fmt.Errorf("securing dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("writing temp file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	// The rename preserves the DACL, so the committed file is never unsecured.
	if err = SecureFile(tmpName); err != nil {
		return fmt.Errorf("securing file: %w", err)
	}
	if err = renameWithRetry(tmpName, path); err != nil {
		return fmt.Errorf("renaming temp file: %w", err)
	}

	syncDir(dir)

	return nil
}

// renameWithRetry retries a transient sharing violation: Windows refuses to
// replace a file another process holds open without FILE_SHARE_DELETE (AV,
// indexer), even though the holder usually lets go within milliseconds.
func renameWithRetry(oldPath, newPath string) (err error) {
	for range 3 {
		if err = os.Rename(oldPath, newPath); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}

	return err
}

// syncDir fsyncs dir so the rename is durable.  Best-effort: opening a
// directory for sync fails on some platforms.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
