package elevate

import "testing"

// TestListPatchURLShape pins that an added filter list goes through the shared
// config validator, so a UNC/device path cannot be handed to the service.
func TestListPatchURLShape(t *testing.T) {
	good := ListPatch{Add: []ListEntry{
		{URL: "https://example.com/hosts.txt"},
		{URL: `C:\lists\hosts.txt`},
	}}
	if err := good.validateShape(); err != nil {
		t.Fatalf("valid list patch rejected: %v", err)
	}

	for _, u := range []string{"", `\\attacker\share\hosts.txt`, `\\.\PhysicalDrive0`, `relative\hosts.txt`} {
		lp := ListPatch{Add: []ListEntry{{URL: u}}}
		if err := lp.validateShape(); err == nil {
			t.Errorf("list url %q accepted", u)
		}
	}
}
