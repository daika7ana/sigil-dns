//go:build !windows

package service

import "testing"

func TestNonWindowsStubs(t *testing.T) {
	if Run(nil) == nil {
		t.Error("Run should be unsupported off Windows")
	}
	if Install() == nil || Start() == nil || Stop() == nil {
		t.Error("service management should be unsupported off Windows")
	}
}
