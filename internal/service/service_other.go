//go:build !windows

package service

// Run runs the service host.  Off Windows there is no SCM.
func Run(agent *Agent) (err error) { return ErrUnsupported }

// Install registers the service.  Off Windows there is no SCM.
func Install() (err error) { return ErrUnsupported }

// InstallExe registers the service against exe.  Off Windows there is no SCM.
func InstallExe(exe string) (err error) { return ErrUnsupported }

// Delete removes the service registration.  Off Windows there is no SCM.
func Delete() (err error) { return ErrUnsupported }

// Executable returns the canonical service binary.  Off Windows there is no SCM.
func Executable() (exe string, err error) { return "", ErrUnsupported }

// Start starts the service.  Off Windows there is no SCM.
func Start() (err error) { return ErrUnsupported }

// Stop stops the service.  Off Windows there is no SCM.
func Stop() (err error) { return ErrUnsupported }
