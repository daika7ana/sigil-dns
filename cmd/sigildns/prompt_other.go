//go:build !windows

package main

import (
	"fmt"
	"os"
)

// promptHidden is the dev/non-Windows fallback: there is no Windows console
// mode to change, so it reads a plain line.  The shipped product is
// Windows-only.
func promptHidden() (id string, err error) {
	fmt.Fprint(os.Stderr, "Resolver id: ")

	return readIDLine(os.Stdin)
}
