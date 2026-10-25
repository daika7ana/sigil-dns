package config

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoad checks that admin-editable YAML can never panic the decode/validate
// path.  Any input may be rejected; only a crash is a failure.  Seeds run as
// ordinary tests, `go test -fuzz=FuzzLoad ./internal/config/` explores more.
func FuzzLoad(f *testing.F) {
	for _, seed := range []string{
		"",
		"schema_version: 1\n",
		"profiles: {}\n",
		"schema_version: 1\nprofile: default\nprofiles:\n  default:\n" +
			"    upstreams: [\"https://dns.quad9.net/dns-query\"]\n" +
			"    bootstrap: [\"9.9.9.9\"]\n" +
			"    cache: {enabled: true, size_bytes: 67108864}\n",
		"profiles:\n  default:\n    per_domain:\n      - domains: [\"\"]\n        upstream: \"#\"\n",
	} {
		f.Add([]byte(seed))
	}

	// One file for the whole run; a real fuzz run rewrites it per input.
	path := filepath.Join(f.TempDir(), "config.yaml")

	f.Fuzz(func(t *testing.T, data []byte) {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}

		_, _ = Load(path)
	})
}
