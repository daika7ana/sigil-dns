//go:build gui && windows

package gui

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"syscall"
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

	f, err := os.CreateTemp("", "sigildns-task-*.xml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()

	if _, err = f.Write(utf16LE(autostartTaskXML(exe, id))); err != nil {
		_ = f.Close()

		return err
	}
	if err = f.Close(); err != nil {
		return err
	}

	return runSchtasks([]string{"/Create", "/TN", autostartTaskName, "/XML", f.Name(), "/F"})
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
