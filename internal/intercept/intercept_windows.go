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

// SnapshotAdapters captures the current per-interface DNS servers.  Exported
// for drift detection; also usable for the per-adapter fallback.
func SnapshotAdapters(ctx context.Context) (entries []AdapterEntry, err error) {
	raw, err := snapshotAdapters(ctx)
	if err != nil {
		return nil, err
	}

	entries = make([]AdapterEntry, 0, len(raw))
	for _, e := range raw {
		entries = append(entries, AdapterEntry{Index: e.Index, IPv4: e.IPv4, IPv6: e.IPv6})
	}

	return entries, nil
}

// ApplyAdapters points both families of each interface at loopback.
func ApplyAdapters(ctx context.Context, entries []AdapterEntry) (err error) {
	return applyAdapters(ctx, toDNSEntries(entries))
}

// ResetAdapters restores the captured per-interface DNS servers.
func ResetAdapters(ctx context.Context, entries []AdapterEntry) (err error) {
	return resetAdapters(ctx, toDNSEntries(entries))
}

// toDNSEntries converts exported entries to the internal form.
func toDNSEntries(entries []AdapterEntry) (out []dnsEntry) {
	out = make([]dnsEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, dnsEntry{Index: e.Index, IPv4: e.IPv4, IPv6: e.IPv6})
	}

	return out
}

// ProbeNRPT reports whether our rule is present and any foreign rules.
func ProbeNRPT(ctx context.Context) (st NRPTState, err error) {
	const script = `
$ErrorActionPreference = 'SilentlyContinue'
$ours = @(Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'dns-client' }).Count
Write-Output "ours=$ours"
Get-DnsClientNrptRule | Where-Object { $_.DisplayName -ne 'dns-client' } | ForEach-Object {
    Write-Output ("foreign={0}|{1}" -f $_.DisplayName, $_.Namespace)
}
`
	out, err := runPS(ctx, script)
	if err != nil {
		return NRPTState{}, err
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "ours=1":
			st.Ours = true
		case strings.HasPrefix(line, "foreign="):
			st.Conflicts = append(st.Conflicts, strings.TrimPrefix(line, "foreign="))
		}
	}

	return st, nil
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
