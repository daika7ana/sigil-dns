//go:build windows

// Package intercept Windows implementation.  It shells out to PowerShell using
// stable cmdlets so no cgo or extra dependency is needed.
package intercept

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// New returns the Windows NRPT controller.
func New(logger *slog.Logger) Controller {
	if logger == nil {
		logger = slog.Default().With("component", "intercept")
	}

	return nrptController{logger: logger}
}

// nrptController manages our NRPT rules.
type nrptController struct {
	logger *slog.Logger
}

// Enable implements Controller.  It is idempotent: any stale rule of ours is
// removed before the catch-all and the bypass exemptions are added.  A bypass
// exemption whose empty-name-server encoding did not take effect falls back to
// pinning the adapter's current DNS servers.
func (c nrptController) Enable(ctx context.Context, bypass []string) (err error) {
	namespaces := normalizeBypass(bypass)

	out, err := runPS(ctx, nrptEnableScript(namespaces))
	if err != nil {
		return err
	}

	missing := nrptVerify(out, namespaces)
	if len(missing) == 0 {
		return nil
	}

	// The empty-name-server encoding is not first-class in the DnsClient
	// cmdlets, so fall back to the adapter's current DNS servers.
	servers := currentDNSServers(ctx)
	if len(servers) == 0 {
		c.warnUnverified(ctx, missing)

		return nil
	}

	fbOut, fErr := runPS(ctx, nrptFallbackScript(missing, servers))
	if fErr != nil {
		c.logger.WarnContext(ctx, "bypass fallback failed", "err", fErr)
		c.warnUnverified(ctx, missing)

		return nil
	}

	// Never fail the whole enable because one bypass could not be applied: the
	// domain simply keeps going through our resolver.
	c.warnUnverified(ctx, nrptVerify(fbOut, missing))

	return nil
}

// warnUnverified logs a warning for each namespace still unverifiable.
func (c nrptController) warnUnverified(ctx context.Context, namespaces []string) {
	for _, ns := range namespaces {
		c.logger.WarnContext(ctx, "bypass namespace unverified", "namespace", ns)
	}
}

// normalizeBypass maps bypass domains to unique NRPT namespaces, dropping empties.
func normalizeBypass(bypass []string) (namespaces []string) {
	seen := map[string]bool{}
	for _, d := range bypass {
		ns := nrptNamespace(d)
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		namespaces = append(namespaces, ns)
	}

	return namespaces
}

// currentDNSServers returns every distinct non-loopback DNS server configured on
// any interface.
func currentDNSServers(ctx context.Context) (servers []string) {
	entries, err := snapshotAdapters(ctx)
	if err != nil {
		return nil
	}

	seen := map[string]bool{}
	for _, e := range entries {
		for _, a := range append(append([]string{}, e.IPv4...), e.IPv6...) {
			a = strings.TrimSpace(a)
			if a == "" || a == "127.0.0.1" || a == "::1" || seen[a] {
				continue
			}
			seen[a] = true
			servers = append(servers, a)
		}
	}

	return servers
}

// Disable implements Controller.  It removes only our rule and is idempotent.
func (nrptController) Disable(ctx context.Context) (err error) {
	const script = `
$ErrorActionPreference = 'SilentlyContinue'
Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'Sigil DNS' } |
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
$ours = @(Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'Sigil DNS' }).Count
Write-Output "ours=$ours"
Get-DnsClientNrptRule | Where-Object { $_.DisplayName -ne 'Sigil DNS' } | ForEach-Object {
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
		// Bypass exemptions make our rule count > 1, so any positive count is ours.
		case strings.HasPrefix(line, "ours="):
			if n, cErr := strconv.Atoi(strings.TrimPrefix(line, "ours=")); cErr == nil && n >= 1 {
				st.Ours = true
			}
		case strings.HasPrefix(line, "foreign="):
			st.Conflicts = append(st.Conflicts, strings.TrimPrefix(line, "foreign="))
		}
	}

	return st, nil
}

// runPS runs a PowerShell command and returns its stdout.
func runPS(ctx context.Context, script string) (out string, err error) {
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	// Keep a console-less GUI/--apply process from flashing a console window.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("powershell: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return stdout.String(), nil
}
