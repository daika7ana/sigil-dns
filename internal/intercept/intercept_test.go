package intercept

import (
	"strings"
	"testing"
)

func TestNrptNamespace(t *testing.T) {
	cases := []struct{ in, want string }{
		{"example.com", ".example.com"},
		{"*.sub.example.com", ".sub.example.com"},
		{"example.com.", ".example.com"},
		{"  *.sub.example.com.  ", ".sub.example.com"},
		{"*", ""},
		{"", ""},
		{"   ", ""},
		{"*.", ""},
	}

	for _, tc := range cases {
		if got := NRPTNamespace(tc.in); got != tc.want {
			t.Errorf("NRPTNamespace(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNrptEnableScript(t *testing.T) {
	script := nrptEnableScript([]string{".a.example", ".b.example"}, []string{"127.0.0.1", "::1"})

	if !strings.Contains(script, "Remove-DnsClientNrptRule -Force") {
		t.Errorf("script lacks the removal preamble:\n%s", script)
	}
	if !strings.Contains(script, "Add-DnsClientNrptRule -Namespace '.' -NameServers '127.0.0.1','::1' -DisplayName 'Sigil DNS'") {
		t.Errorf("script lacks the catch-all with the bound servers:\n%s", script)
	}
	for _, ns := range []string{"'.a.example'", "'.b.example'"} {
		if !strings.Contains(script, "Add-DnsClientNrptRule -Namespace "+ns+" -DisplayName 'Sigil DNS'") {
			t.Errorf("script lacks exemption for %s:\n%s", ns, script)
		}
	}
	if strings.Contains(script, "-Namespace '.a.example' -NameServers") {
		t.Errorf("exemption must not set name servers:\n%s", script)
	}
	// A rejected exemption must not abort the script (verification/fallback runs).
	if !strings.Contains(script, "try { Add-DnsClientNrptRule -Namespace '.a.example'") {
		t.Errorf("exemption must be guarded so one rejection cannot abort enable:\n%s", script)
	}
	if !strings.Contains(script, "Write-Output ('rule=' + ($_.Namespace -join ','))") {
		t.Errorf("script lacks the rule= echo:\n%s", script)
	}
}

// TestNrptProbeScriptJoinsArrays guards the probe's output format.  Namespace
// is a CIM String[], and `'{0}' -f $_.Namespace` renders it as
// "System.String[]", which made every probe look like drift (the catch-all
// namespace never matched) and auto-disabled interception after three tries.
func TestNrptProbeScriptJoinsArrays(t *testing.T) {
	for _, want := range []string{
		"($_.Namespace -join ',')",
		"($_.NameServers -join ',')",
	} {
		if !strings.Contains(nrptProbeScript, want) {
			t.Errorf("probe script lacks %q:\n%s", want, nrptProbeScript)
		}
	}
	if strings.Contains(nrptProbeScript, "-f $_.Namespace,") {
		t.Errorf("probe script formats the namespace array directly:\n%s", nrptProbeScript)
	}
}

func TestNrptEnableScriptServers(t *testing.T) {
	// No bound servers (legacy caller) keeps the dual-stack default.
	if got := nrptEnableScript(nil, nil); !strings.Contains(got, "-NameServers '127.0.0.1','::1'") {
		t.Errorf("empty servers did not default to dual-stack:\n%s", got)
	}

	// IPv4-only must advertise only that address.
	if got := nrptEnableScript(nil, []string{"127.0.0.1"}); !strings.Contains(got, "-NameServers '127.0.0.1' -DisplayName") {
		t.Errorf("IPv4-only bind advertised extra servers:\n%s", got)
	}
}

func TestNrptVerify(t *testing.T) {
	out := "rule=.a.example\nrule=sub.example\n"
	missing := nrptVerify(out, []string{".a.example", ".b.example", ".sub.example"})

	if len(missing) != 1 || missing[0] != ".b.example" {
		t.Fatalf("missing = %v, want [.b.example]", missing)
	}
}

func TestNrptFallbackScript(t *testing.T) {
	script := nrptFallbackScript([]string{".a.example"}, []string{"9.9.9.9", "149.112.112.112"})

	rm := strings.Index(script, "Remove-DnsClientNrptRule")
	add := strings.Index(script, "Add-DnsClientNrptRule -Namespace '.a.example' -NameServers '9.9.9.9','149.112.112.112' -DisplayName 'Sigil DNS'")
	if rm < 0 || add < 0 {
		t.Fatalf("fallback script missing remove or add:\n%s", script)
	}
	if rm > add {
		t.Errorf("fallback must remove the old rule before adding:\n%s", script)
	}
	if !strings.Contains(script, "$_.Namespace -eq '.a.example'") {
		t.Errorf("remove must target the exact namespace:\n%s", script)
	}
	// A rejected removal must abort the fallback, or the duplicate rule left
	// behind silently keeps the exemption unverified.
	if strings.Contains(script, "SilentlyContinue") {
		t.Errorf("fallback removal must not swallow errors:\n%s", script)
	}
}
