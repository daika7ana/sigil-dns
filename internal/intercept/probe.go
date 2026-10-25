package intercept

import (
	"errors"
	"strings"
)

// errProbeIncomplete means the probe did not finish enumerating rules.
var errProbeIncomplete = errors.New("nrpt probe did not confirm enumeration")

// parseNRPTProbe parses the PowerShell probe output: one rule=<namespace>|<servers>
// line per own rule, foreign=<display>|<namespace> lines, and the ok=1 sentinel.
// A missing sentinel is an error: it means enumeration did not complete, not
// that no rules exist.
func parseNRPTProbe(out string) (st NRPTState, err error) {
	st.Servers = map[string][]string{}
	sentinel := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "rule="):
			// rule=<namespace>|<servers>; the catch-all normalizes to "".
			parts := strings.SplitN(strings.TrimPrefix(line, "rule="), "|", 2)
			ns := nrptNormalize(parts[0])
			st.Namespaces = append(st.Namespaces, ns)

			var servers []string
			if len(parts) == 2 && parts[1] != "" {
				for _, s := range strings.Split(parts[1], ",") {
					if s = strings.TrimSpace(s); s != "" {
						servers = append(servers, s)
					}
				}
			}
			st.Servers[ns] = servers
		case strings.HasPrefix(line, "foreign="):
			st.Conflicts = append(st.Conflicts, strings.TrimPrefix(line, "foreign="))
		case line == "ok=1":
			sentinel = true
		}
	}
	if !sentinel {
		return NRPTState{}, errProbeIncomplete
	}
	st.Ours = len(st.Namespaces) > 0

	return st, nil
}
