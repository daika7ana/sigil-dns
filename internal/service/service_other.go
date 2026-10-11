//go:build !windows

package service

// Run runs the service host.  Off Windows there is no SCM.
func Run(agent *Agent) (err error) { return ErrUnsupported }

// Install registers the service.  Off Windows there is no SCM.
func Install() (err error) { return ErrUnsupported }

// Uninstall removes the service.  Off Windows there is no SCM.
func Uninstall() (err error) { return ErrUnsupported }

// Start starts the service.  Off Windows there is no SCM.
func Start() (err error) { return ErrUnsupported }

// Stop stops the service.  Off Windows there is no SCM.
func Stop() (err error) { return ErrUnsupported }
