// Package intercept applies and removes the system DNS interception.
//
// On Windows the primary mechanism is a single NRPT rule covering all
// namespaces; a per-adapter loopback fallback is also implemented but not wired
// into the default path.  On other platforms a stub returns ErrUnsupported so
// Linux builds and tests compile.
package intercept

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported is returned by the non-Windows stub.
var ErrUnsupported = errors.New("dns interception is not supported on this platform")

// Controller applies and removes the DNS interception.
type Controller interface {
	// Enable turns interception on, exempting the bypass namespaces (domains
	// to resolve with the system's own DNS).  It must be idempotent.
	Enable(ctx context.Context, bypass []string) error

	// Disable removes interception.  It must be idempotent.
	Disable(ctx context.Context) error
}

// AdapterEntry is a per-interface DNS configuration.  It is exported so drift
// detection can reuse the snapshot/apply/reset helpers.
type AdapterEntry struct {
	// IPv4 and IPv6 are the configured DNS servers per family.
	IPv4 []string
	IPv6 []string

	// Index is the interface index.
	Index int
}

// NRPTState is the observed NRPT state: whether our rule is present and the
// foreign rules that may conflict.
type NRPTState struct {
	// Conflicts are foreign equal-or-more-specific rules (DisplayName|Namespace).
	Conflicts []string

	// Ours is true when our own rule is present.
	Ours bool
}

// nrptNamespace normalizes a domain to NRPT suffix form (leading dot), or ""
// when nothing usable remains.
func nrptNamespace(domain string) string {
	name := strings.TrimSuffix(strings.TrimSpace(domain), ".")
	name = strings.TrimPrefix(name, "*.")
	if name == "*" {
		name = ""
	}
	if name == "" {
		return ""
	}

	return "." + name
}

// nrptNormalize reduces an NRPT namespace to its dot-free comparison key.
func nrptNormalize(ns string) string { return strings.Trim(strings.TrimSpace(ns), ".") }

// nrptEnableScript builds the PowerShell that re-adds our catch-all rule plus an
// exemption (no name servers) per bypass namespace, then prints one rule=<ns>
// line per our rule so the caller can verify what actually took effect.
func nrptEnableScript(namespaces []string) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	b.WriteString("Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'Sigil DNS' } | Remove-DnsClientNrptRule -Force -ErrorAction SilentlyContinue\n")
	b.WriteString("Add-DnsClientNrptRule -Namespace '.' -NameServers 127.0.0.1,'::1' -DisplayName 'Sigil DNS' | Out-Null\n")
	for _, ns := range namespaces {
		// A rejected exemption must not abort the whole enable: verification
		// below reports it and the fallback retries it with explicit servers.
		fmt.Fprintf(&b, "try { Add-DnsClientNrptRule -Namespace %s -DisplayName 'Sigil DNS' -ErrorAction Stop | Out-Null } catch { }\n", psQuote(ns))
	}
	b.WriteString(nrptPrintRules)

	return b.String()
}

// nrptVerify returns the namespaces that do not appear in the printed rule=
// lines, comparing on the dot-normalized form.
func nrptVerify(out string, namespaces []string) (missing []string) {
	present := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		ns, ok := strings.CutPrefix(strings.TrimSpace(line), "rule=")
		if ok {
			present[nrptNormalize(ns)] = true
		}
	}
	for _, ns := range namespaces {
		if !present[nrptNormalize(ns)] {
			missing = append(missing, ns)
		}
	}

	return missing
}

// nrptFallbackScript pins the given DNS servers on each namespace that could not
// be exempted.  The existing rule is removed first: two rules on one namespace
// cancel each other out.
func nrptFallbackScript(namespaces []string, servers []string) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	for _, ns := range namespaces {
		fmt.Fprintf(&b, "Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'Sigil DNS' -and $_.Namespace -eq %s } | Remove-DnsClientNrptRule -Force -ErrorAction SilentlyContinue\n", psQuote(ns))
		fmt.Fprintf(&b, "try { Add-DnsClientNrptRule -Namespace %s -NameServers %s -DisplayName 'Sigil DNS' -ErrorAction Stop | Out-Null } catch { }\n", psQuote(ns), psList(servers))
	}
	b.WriteString(nrptPrintRules)

	return b.String()
}

// nrptPrintRules emits one rule=<Namespace> line per rule we own.
const nrptPrintRules = "Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'Sigil DNS' } | ForEach-Object { Write-Output ('rule=' + $_.Namespace) }\n"

// psQuote renders s as a single-quoted PowerShell string literal.
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// psList renders addrs as a PowerShell array literal with each item quoted.
func psList(addrs []string) (list string) {
	quoted := make([]string, len(addrs))
	for i, a := range addrs {
		quoted[i] = psQuote(a)
	}

	return strings.Join(quoted, ",")
}
