//go:build gui && windows

package gui

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"syscall"

	"github.com/daika7ana/sigil-dns/internal/config"
)

// setStartWithWindows registers or removes the per-user logon task.  The GUI is
// always elevated, so creating the task does not prompt; the XML's principal is
// this user with InteractiveToken (no stored password) and HighestAvailable, so
// Task Scheduler starts the tray at sign-in elevated, with no UAC prompt.
func setStartWithWindows(on bool) error {
	if !on {
		return deleteAutostartTask()
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	u, uErr := user.Current()
	if uErr != nil {
		return uErr
	}
	// The SID is locale-independent and survives an account rename; os/user
	// fills Uid with it on Windows.  Fall back to DOMAIN\user if it is empty.
	id := u.Uid
	if id == "" {
		id = u.Username
	}

	path, err := stageTaskXML(autostartTaskXML(exe, id))
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path) }()

	return runSchtasks([]string{"/Create", "/TN", autostartTaskName, "/XML", path, "/F"})
}

// stageTaskXML writes the task XML into the machine-wide data directory
// (SYSTEM + Administrators only) and returns its path.  %TEMP% is
// user-writable, so a same-user process could swap the file before schtasks
// read it and register an arbitrary exe at logon with HighestAvailable.
func stageTaskXML(xml string) (path string, err error) {
	dir := config.SystemDir()
	if dir == "" {
		return "", fmt.Errorf("sigildns: cannot resolve the machine-wide data directory")
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	return writeTaskXML(dir, xml)
}

// writeTaskXML stages the UTF-16LE task XML in dir and returns the file path.
func writeTaskXML(dir, xml string) (path string, err error) {
	f, err := os.CreateTemp(dir, "sigildns-task-*.xml")
	if err != nil {
		return "", err
	}
	path = f.Name()

	if _, err = f.Write(utf16LE(xml)); err != nil {
		_ = f.Close()
		_ = os.Remove(path)

		return "", err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(path)

		return "", err
	}

	return path, nil
}

// deleteAutostartTask removes the logon task.  A delete that fails because the
// task was never created is success; a delete refused while the task still
// exists is an error, so the caller keeps the preference set instead of
// promising a removal that did not happen.
func deleteAutostartTask() error {
	delErr := runSchtasks([]string{"/Delete", "/TN", autostartTaskName, "/F"})
	if delErr == nil {
		return nil
	}
	if runSchtasks([]string{"/Query", "/TN", autostartTaskName}) != nil {
		return nil // gone, or never there
	}

	return delErr
}

func runSchtasks(args []string) error {
	cmd := exec.Command("schtasks", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}
