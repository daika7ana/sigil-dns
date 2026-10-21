// Package health implements the two-stage health check required before
// interception is applied.
//
// Stage A queries a random synthetic name that only our resolver's handler
// answers, proving the loopback endpoint is ours and not another process bound
// to :53.  Stage B queries a random uncached name per address family to prove
// the upstream works, catching the IPv6-dead-while-IPv4-ok case.
package health

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// DefaultTimeout is the per-query timeout.
const DefaultTimeout = 5 * time.Second

// Target describes the resolver to check.
type Target struct {
	// Synthetic is the per-process probe name answered only by our handler.
	Synthetic string

	// UDPAddrs are the bound loopback UDP listen addresses.
	UDPAddrs []string

	// Timeout is the per-query timeout; DefaultTimeout if zero.
	Timeout time.Duration
}

// Result is the outcome of a two-stage health check.
type Result struct {
	Detail string
	Ours   bool
	IPv4   bool
	IPv6   bool
	OK     bool
}

// RandomName returns a random uncached probe name.
func RandomName() (name string) { return randomLabel() + ".sigildns-health.invalid." }

// randomLabel returns a random 16-character hex label.
func randomLabel() (label string) {
	var b [8]byte
	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}

// Check runs both stages against t.
func Check(ctx context.Context, t Target) (r Result) {
	if t.Timeout <= 0 {
		t.Timeout = DefaultTimeout
	}

	var details []string
	r.Ours = true
	v4present, v6present := false, false

	for _, addr := range t.UDPAddrs {
		isV4 := isIPv4(addr)
		if isV4 {
			v4present = true
		} else {
			v6present = true
		}

		qtype := dns.TypeAAAA
		if isV4 {
			qtype = dns.TypeA
		}

		// Stage A: prove the endpoint is ours.
		respA, err := exchange(ctx, addr, t.Synthetic, qtype, t.Timeout)
		if err != nil || respA.Rcode != dns.RcodeSuccess || len(respA.Answer) == 0 {
			r.Ours = false
			details = append(details, fmt.Sprintf("stage A %s: not ours (%v)", addr, firstErr(err)))

			continue
		}

		// Stage B: prove the upstream works for this family.  Any response
		// (including NXDOMAIN) means the upstream answered.
		respB, err := exchange(ctx, addr, RandomName(), qtype, t.Timeout)
		ok := err == nil && (respB.Rcode == dns.RcodeSuccess || respB.Rcode == dns.RcodeNameError)
		if isV4 {
			r.IPv4 = ok
		} else {
			r.IPv6 = ok
		}
		if !ok {
			details = append(details,
				fmt.Sprintf("stage B %s: upstream unreachable (%v)", addr, firstErr(err)))
		}
	}

	// Enabling may proceed as long as loopback is ours and IPv4 forwarding
	// works.  A dead IPv6 path is surfaced but does not abort (§4.4).
	r.OK = r.Ours && (v4present && r.IPv4 || !v4present && v6present && r.IPv6)

	if len(details) == 0 {
		r.Detail = "ok"
	} else {
		r.Detail = strings.Join(details, "; ")
	}

	return r
}

// exchange performs a single UDP DNS query.
func exchange(ctx context.Context, addr, name string, qtype uint16, timeout time.Duration) (resp *dns.Msg, err error) {
	c := &dns.Client{Net: "udp", Timeout: timeout}
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)

	resp, _, err = c.ExchangeContext(ctx, m, addr)

	return resp, err
}

// isIPv4 reports whether the host part of addr is an IPv4 address.
func isIPv4(addr string) (ok bool) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)

	return ip != nil && ip.To4() != nil
}

// firstErr renders err for the detail string.
func firstErr(err error) string {
	if err == nil {
		return "no usable answer"
	}

	return err.Error()
}
