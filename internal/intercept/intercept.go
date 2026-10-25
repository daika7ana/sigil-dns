// Package intercept applies and removes the system DNS interception.
//
// On Windows the mechanism is a single NRPT rule covering all namespaces, plus
// exemptions per bypass namespace.  On other platforms a stub returns
// ErrUnsupported so Linux builds and tests compile.
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
	// Enable turns interception on, sending queries to servers (IP literals)
	// and exempting the bypass namespaces (domains to resolve with the
	// system's own DNS).  An empty servers list means the dual-stack loopback
	// default.  It must be idempotent.
	Enable(ctx context.Context, bypass []string, servers []string) error

	// Disable removes interception.  It must be idempotent.
	Disable(ctx context.Context) error
}

// NRPTState is the observed NRPT state: whether our rule is present and the
// foreign rules that may conflict.
type NRPTState struct {
	// Namespaces are our own rules' namespaces, dot-normalized.
	Namespaces []string

	// Servers maps each own rule's normalized namespace to its NameServers.
	// A bypass exemption has an empty (nil) list.
	Servers map[string][]string

	// Conflicts are foreign equal-or-more-specific rules (DisplayName|Namespace).
	Conflicts []string

	// Ours is true when any of our own rules is present.
	Ours bool
}

// NRPTNamespace normalizes a bypass domain to its NRPT suffix form (leading
// dot), or "" when nothing usable remains.  Drift uses the same mapping when it
// builds the expected rule set.
func NRPTNamespace(domain string) string {
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

// nrptEnableScript builds the PowerShell that re-adds our catch-all rule on
// servers plus an exemption (no name servers) per bypass namespace, then prints
// one rule=<ns> line per our rule so the caller can verify what actually took
// effect.
func nrptEnableScript(namespaces, servers []string) string {
	if len(servers) == 0 {
		servers = []string{"127.0.0.1", "::1"}
	}

	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	b.WriteString("Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'Sigil DNS' } | Remove-DnsClientNrptRule -Force\n")
	fmt.Fprintf(&b, "Add-DnsClientNrptRule -Namespace '.' -NameServers %s -DisplayName 'Sigil DNS' | Out-Null\n", psList(servers))
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
		fmt.Fprintf(&b, "Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'Sigil DNS' -and $_.Namespace -eq %s } | Remove-DnsClientNrptRule -Force\n", psQuote(ns))
		fmt.Fprintf(&b, "try { Add-DnsClientNrptRule -Namespace %s -NameServers %s -DisplayName 'Sigil DNS' -ErrorAction Stop | Out-Null } catch { }\n", psQuote(ns), psList(servers))
	}
	b.WriteString(nrptPrintRules)

	return b.String()
}

// nrptPrintRules emits one rule=<Namespace> line per rule we own.
const nrptPrintRules = "Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'Sigil DNS' } | ForEach-Object { Write-Output ('rule=' + ($_.Namespace -join ',')) }\n"

// nrptProbeScript enumerates the NRPT rules for ProbeNRPT: one
// rule=<namespace>|<servers> line per rule we own, foreign=<display>|<namespace>
// lines, and the ok=1 sentinel.  Namespace and NameServers are CIM String[]
// properties: the format operator renders an array as "System.String[]", so
// both must be joined explicitly.
const nrptProbeScript = `
$ErrorActionPreference = 'Stop'
try {
    $rules = @(Get-DnsClientNrptRule)
    $rules | Where-Object { $_.DisplayName -eq 'Sigil DNS' } | ForEach-Object {
        Write-Output ('rule={0}|{1}' -f ($_.Namespace -join ','), ($_.NameServers -join ','))
    }
    $rules | Where-Object { $_.DisplayName -ne 'Sigil DNS' } | ForEach-Object {
        Write-Output ("foreign={0}|{1}" -f $_.DisplayName, ($_.Namespace -join ','))
    }
    Write-Output 'ok=1'
} catch {
    Write-Error $_
    exit 1
}
`

// psMessageMax bounds the condensed PowerShell error text.
const psMessageMax = 300

// psErrorMessage condenses PowerShell stderr to the one useful line: Windows
// PowerShell's normal error view echoes the whole failing command (for -Command
// that is the entire script) followed by a position block and "CategoryInfo"
// scaffolding.  The message is the line above the position block, after the
// echoed command and its " : " separator.
//
// The block is found by the unindented "+" marker that follows it, not by its
// header text: "At line:" is localized ("In Zeile:", "Au niveau de la ligne
// …").  The header is exactly one line, hence the two-line step back.
func psErrorMessage(stderr string) (msg string) {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i, ln := range lines {
		if i >= 2 && strings.HasPrefix(ln, "+") {
			msg = lines[i-2]

			break
		}
	}
	if msg == "" {
		for i, ln := range lines {
			if i > 0 && strings.HasPrefix(strings.TrimSpace(ln), "At line:") {
				msg = lines[i-1]

				break
			}
		}
	}
	if msg == "" && len(lines) > 0 {
		msg = lines[0]
	}
	if _, after, ok := strings.Cut(msg, " : "); ok {
		msg = after
	}
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > psMessageMax {
		msg = string(r[:psMessageMax]) + "…"
	}

	return msg
}

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
