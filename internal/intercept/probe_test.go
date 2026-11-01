package intercept

import (
	"errors"
	"slices"
	"testing"
)

func TestParseNRPTProbe(t *testing.T) {
	tests := []struct {
		name       string
		out        string
		wantOurs   bool
		wantNS     []string
		wantServer map[string][]string
		wantConf   []string
		wantCatch  bool
		wantErr    bool
	}{
		{
			name:       "catch-all and bypass",
			out:        "rule=.|127.0.0.1,::1\nrule=.corp.example|\nforeign=VPN|.\nok=1\n",
			wantOurs:   true,
			wantNS:     []string{"", "corp.example"},
			wantServer: map[string][]string{"": {"127.0.0.1", "::1"}, "corp.example": {}},
			wantConf:   []string{"VPN|."},
			wantCatch:  true,
		},
		{
			name:       "specific foreign rule only",
			out:        "rule=.|127.0.0.1\nforeign=VPN|.corp.example\nok=1\n",
			wantOurs:   true,
			wantNS:     []string{""},
			wantServer: map[string][]string{"": {"127.0.0.1"}},
			wantConf:   []string{"VPN|.corp.example"},
			wantCatch:  false,
		},
		{
			name:       "crlf and whitespace",
			out:        "  rule=.corp.example|127.0.0.1  \r\nok=1\r\n",
			wantOurs:   true,
			wantNS:     []string{"corp.example"},
			wantServer: map[string][]string{"corp.example": {"127.0.0.1"}},
		},
		{
			name:       "no rules",
			out:        "ok=1\n",
			wantOurs:   false,
			wantServer: map[string][]string{},
		},
		{
			name:    "missing sentinel",
			out:     "rule=.|127.0.0.1\n",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseNRPTProbe(tc.out)
			if tc.wantErr {
				if !errors.Is(err, errProbeIncomplete) {
					t.Fatalf("parseNRPTProbe = %v, want errProbeIncomplete", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("parseNRPTProbe: %v", err)
			}
			if got.Ours != tc.wantOurs {
				t.Errorf("Ours = %v, want %v", got.Ours, tc.wantOurs)
			}
			if !slices.Equal(got.Namespaces, tc.wantNS) {
				t.Errorf("Namespaces = %q, want %q", got.Namespaces, tc.wantNS)
			}
			if len(got.Servers) != len(tc.wantServer) {
				t.Errorf("Servers = %v, want %v", got.Servers, tc.wantServer)
			}
			for ns, want := range tc.wantServer {
				if !slices.Equal(got.Servers[ns], want) {
					t.Errorf("Servers[%q] = %q, want %q", ns, got.Servers[ns], want)
				}
			}
			if !slices.Equal(got.Conflicts, tc.wantConf) {
				t.Errorf("Conflicts = %q, want %q", got.Conflicts, tc.wantConf)
			}
			if got.CatchAllConflict != tc.wantCatch {
				t.Errorf("CatchAllConflict = %v, want %v", got.CatchAllConflict, tc.wantCatch)
			}
		})
	}
}
