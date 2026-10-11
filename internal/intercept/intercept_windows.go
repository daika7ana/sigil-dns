//go:build windows

// Package intercept Windows implementation.  It shells out to PowerShell using
// stable cmdlets so no cgo or extra dependency is needed.
package intercept

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// New returns the Windows NRPT controller.
func New() Controller { return nrptController{} }

// nrptController manages our single NRPT rule.
type nrptController struct{}

// Enable implements Controller.  It is idempotent: any stale rule of ours is
// removed before the rule is added.
func (nrptController) Enable(ctx context.Context) (err error) {
	const script = `
$ErrorActionPreference = 'Stop'
Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'dns-client' } |
    Remove-DnsClientNrptRule -Force -ErrorAction SilentlyContinue
Add-DnsClientNrptRule -Namespace '.' -NameServers 127.0.0.1,'::1' -DisplayName 'dns-client' | Out-Null
`
	_, err = runPS(ctx, script)

	return err
}

// Disable implements Controller.  It removes only our rule and is idempotent.
func (nrptController) Disable(ctx context.Context) (err error) {
	const script = `
$ErrorActionPreference = 'SilentlyContinue'
Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'dns-client' } |
    Remove-DnsClientNrptRule -Force
`
	_, err = runPS(ctx, script)

	return err
}

// Status implements Controller.
func (nrptController) Status(ctx context.Context) (st Status, err error) {
	const script = `
$c = @(Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'dns-client' }).Count
Write-Output $c
`
	out, err := runPS(ctx, script)
	if err != nil {
		return Status{}, err
	}

	n, _ := strconv.Atoi(strings.TrimSpace(out))
	st = Status{Enabled: n > 0, Mechanism: "nrpt"}
	if st.Enabled {
		st.Detail = fmt.Sprintf("NRPT rule %q active", DisplayName)
	}

	return st, nil
}

// dnsEntry is one interface's captured DNS servers, used by the per-adapter
// fallback mechanism.
type dnsEntry struct {
	IPv4  []string
	IPv6  []string
	Index int
}

// snapshotAdapters captures the per-interface DNS servers for every interface.
// It backs the per-adapter fallback and is not wired into the default path yet.
func snapshotAdapters(ctx context.Context) (entries []dnsEntry, err error) {
	const script = `
Get-DnsClientServerAddress -ErrorAction SilentlyContinue | ForEach-Object {
    '{0}|{1}|{2}' -f $_.InterfaceIndex, $_.AddressFamily, ($_.ServerAddresses -join ',')
}
`
	out, err := runPS(ctx, script)
	if err != nil {
		return nil, err
	}

	byIndex := map[int]*dnsEntry{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 3 {
			continue
		}

		idx, cErr := strconv.Atoi(parts[0])
		if cErr != nil {
			continue
		}

		e := byIndex[idx]
		if e == nil {
			e = &dnsEntry{Index: idx}
			byIndex[idx] = e
		}

		var addrs []string
		if parts[2] != "" {
			addrs = strings.Split(parts[2], ",")
		}

		// AddressFamily: 2 = IPv4, 23 = IPv6.
		switch parts[1] {
		case "2":
			e.IPv4 = addrs
		case "23":
			e.IPv6 = addrs
		}
	}

	for _, e := range byIndex {
		entries = append(entries, *e)
	}

	return entries, nil
}

// applyAdapters points both families of each captured interface at loopback.
// Fallback mechanism; not wired into the default path yet.
func applyAdapters(ctx context.Context, entries []dnsEntry) (err error) {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "Set-DnsClientServerAddress -InterfaceIndex %d -AddressFamily IPv4 -ServerAddresses 127.0.0.1\n", e.Index)
		fmt.Fprintf(&b, "Set-DnsClientServerAddress -InterfaceIndex %d -AddressFamily IPv6 -ServerAddresses '::1'\n", e.Index)
	}

	_, err = runPS(ctx, b.String())

	return err
}

// resetAdapters restores the captured per-interface DNS servers.  An empty
// family means the interface was DHCP-assigned, so it is reset.  Fallback
// mechanism; not wired into the default path yet.
func resetAdapters(ctx context.Context, entries []dnsEntry) (err error) {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	for _, e := range entries {
		if len(e.IPv4) > 0 {
			fmt.Fprintf(&b, "Set-DnsClientServerAddress -InterfaceIndex %d -AddressFamily IPv4 -ServerAddresses %s\n",
				e.Index, psList(e.IPv4))
		} else {
			fmt.Fprintf(&b, "Set-DnsClientServerAddress -InterfaceIndex %d -AddressFamily IPv4 -ResetServerAddresses\n", e.Index)
		}
		if len(e.IPv6) > 0 {
			fmt.Fprintf(&b, "Set-DnsClientServerAddress -InterfaceIndex %d -AddressFamily IPv6 -ServerAddresses %s\n",
				e.Index, psList(e.IPv6))
		} else {
			fmt.Fprintf(&b, "Set-DnsClientServerAddress -InterfaceIndex %d -AddressFamily IPv6 -ResetServerAddresses\n", e.Index)
		}
	}

	_, err = runPS(ctx, b.String())

	return err
}

// psList renders addrs as a PowerShell array literal with each item quoted.
func psList(addrs []string) (list string) {
	quoted := make([]string, len(addrs))
	for i, a := range addrs {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", "''") + "'"
	}

	return strings.Join(quoted, ",")
}

// runPS runs a PowerShell command and returns its stdout.
func runPS(ctx context.Context, script string) (out string, err error) {
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("powershell: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return stdout.String(), nil
}
