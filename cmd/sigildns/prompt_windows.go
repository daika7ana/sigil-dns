//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// promptHidden reads the resolver id from the console with echo disabled, like
// an ssh password prompt.  When stdin is not a console (a pipe or a file) it
// reads a plain line without a prompt, so --id still works non-interactively.
func promptHidden() (id string, err error) {
	h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return readIDLine(os.Stdin)
	}

	var mode uint32
	if err = windows.GetConsoleMode(h, &mode); err != nil {
		// Not a console: no prompt, plain line.
		return readIDLine(os.Stdin)
	}

	if err = windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return "", err
	}
	defer func() {
		_ = windows.SetConsoleMode(h, mode)

		// Enter was not echoed; end the prompt line.
		fmt.Fprintln(os.Stderr)
	}()

	fmt.Fprint(os.Stderr, "Resolver id: ")

	return readIDLine(os.Stdin)
}
