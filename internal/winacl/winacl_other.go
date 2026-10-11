//go:build !windows

// Package winacl restricts files and directories to SYSTEM + Administrators on
// Windows.  It is a no-op on other platforms.  Callers are responsible for
// treating an error as fatal when the protected data is sensitive (resolver
// ids, snapshots, keys).
package winacl

// SecureFile is a no-op off Windows.
func SecureFile(path string) (err error) { return nil }

// SecureDir is a no-op off Windows.
func SecureDir(path string) (err error) { return nil }
