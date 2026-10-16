//go:build !windows

package elevate

import (
	"context"
	"errors"
	"testing"
)

func TestApplyUnsupportedOffWindows(t *testing.T) {
	if _, err := Apply(context.Background(), Request{Action: ActionEnable, UUID: "u"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Apply = %v, want ErrUnsupported", err)
	}

	if err := HelperMain("ignored"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("HelperMain = %v, want ErrUnsupported", err)
	}

	if err := ServiceUninstall(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ServiceUninstall = %v, want ErrUnsupported", err)
	}
}
