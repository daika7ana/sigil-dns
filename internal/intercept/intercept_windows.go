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
// removed before the catch-all and the bypass exemptions are added.  servers
// are the resolver's bound addresses; an empty list keeps the dual-stack
// default.  A bypass exemption whose empty-name-server encoding did not take
// effect falls back to pinning the adapter's current DNS servers.
func (c nrptController) Enable(ctx context.Context, bypass, servers []string) (err error) {
	namespaces := normalizeBypass(bypass)

	out, err := runPS(ctx, nrptEnableScript(namespaces, servers))
	if err != nil {
		return err
	}

	missing := nrptVerify(out, namespaces)
	if len(missing) == 0 {
		return nil
	}

	// The empty-name-server encoding is not first-class in the DnsClient
	// cmdlets, so fall back to the adapter's current DNS servers.
	adapterServers := currentDNSServers(ctx)
	if len(adapterServers) == 0 {
		c.warnUnverified(ctx, missing)

		return nil
	}

	fbOut, fErr := runPS(ctx, nrptFallbackScript(missing, adapterServers))
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
		ns := NRPTNamespace(d)
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

// Disable implements Controller.  It removes only our rules and is idempotent.
// Removal is proven by re-enumerating: a surviving rule exits non-zero, so the
// caller keeps the resolver up rather than pointing DNS at a dead port.
func (nrptController) Disable(ctx context.Context) (err error) {
	const script = `
$ErrorActionPreference = 'Stop'
try {
    Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'Sigil DNS' } |
        Remove-DnsClientNrptRule -Force -ErrorAction Stop
    $left = @(Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq 'Sigil DNS' }).Count
    if ($left -gt 0) {
        Write-Output "remaining=$left"
        exit 1
    }
    Write-Output 'ok=1'
} catch {
    Write-Error $_
    exit 1
}
`
	_, err = runPS(ctx, script)

	return err
}

// dnsEntry is one interface's captured DNS servers.
type dnsEntry struct {
	IPv4  []string
	IPv6  []string
	Index int
}

// snapshotAdapters reads the interface DNS servers used by the NRPT bypass
// fallback.
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

// ProbeNRPT reports our rules' namespaces and any foreign rules.  Failure to
// enumerate is an error, never an empty (false-negative) state.
func ProbeNRPT(ctx context.Context) (st NRPTState, err error) {
	out, err := runPS(ctx, nrptProbeScript)
	if err != nil {
		return NRPTState{}, err
	}

	return parseNRPTProbe(out)
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
