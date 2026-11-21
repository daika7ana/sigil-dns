//go:build !windows

// Package winacl restricts files and directories to SYSTEM + Administrators on
// Windows.  It is a no-op on other platforms.  Callers are responsible for
// treating an error as fatal when the protected data is sensitive (resolver
// ids, snapshots, keys).
package winacl

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrHardlink is returned by RefuseHardlink for a multiply-linked file.
var ErrHardlink = errors.New("winacl: refusing a multiply-linked file")

// RefuseHardlink rejects f when it has more than one hard link.  The
// non-Windows build has the same guard so the state lock and log tests can run
// on the Linux host.
func RefuseHardlink(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("winacl: reading link count: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("winacl: link count unavailable")
	}
	if st.Nlink > 1 {
		return ErrHardlink
	}

	return nil
}

// SecureFile is a no-op off Windows.
func SecureFile(path string) (err error) { return nil }

// SecureDir is a no-op off Windows.
func SecureDir(path string) (err error) { return nil }
