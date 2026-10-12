package winacl

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path via a temp file in the same directory,
// fsyncs it, renames it into place, fsyncs the directory, then restricts the
// file to SYSTEM + Administrators.  The directory is created and secured before
// the temp file exists so it never inherits a permissive ACL.  It is shared by
// the config and state writers.
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
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming temp file: %w", err)
	}

	syncDir(dir)

	if err = SecureFile(path); err != nil {
		return fmt.Errorf("securing file: %w", err)
	}

	return nil
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
