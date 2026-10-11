// Package resolver owns the embedded dnsproxy Proxy: it binds the loopback
// listeners, carries the upstream configuration, and answers DNS over plain
// UDP/TCP on 127.0.0.1:53 and [::1]:53.
package resolver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/miekg/dns"
)

// Defaults for the walking skeleton.
const (
	// DefaultUpstream is ControlD's free unfiltered endpoint; it needs no
	// account id.
	DefaultUpstream = "https://freedns.controld.com/p0"

	// DefaultBootstrap is ControlD's free IPv4 bootstrap address.
	DefaultBootstrap = "76.76.2.11:53"

	// DefaultCacheSizeBytes is 64 MiB.
	DefaultCacheSizeBytes = 64 << 20

	// probeIPv4 is answered for synthetic-name A queries.
	probeIPv4 = "192.0.2.53"

	// probeIPv6 is answered for synthetic-name AAAA queries.
	probeIPv6 = "2001:db8::53"
)

// DefaultListen is the dual-stack loopback listener set.
var DefaultListen = []string{"127.0.0.1:53", "[::1]:53"}

// Config configures a Resolver.
type Config struct {
	Logger         *slog.Logger
	Upstreams      []string
	Bootstrap      []string
	Listen         []string
	CacheSizeBytes int
	CacheDisabled  bool
}

// Resolver is a running (or startable) loopback DNS resolver.
type Resolver struct {
	logger        *slog.Logger
	upstreamConf  *proxy.UpstreamConfig
	handler       *probeHandler
	proxy         *proxy.Proxy
	bootstrap     []*upstream.UpstreamResolver
	listen        []string
	cacheSize     int
	cacheDisabled bool
}

// New builds a Resolver.  It creates the upstreams but does not bind anything
// until Start is called.
func New(cfg Config) (r *Resolver, err error) {
	r = &Resolver{
		logger:        loggerOrDefault(cfg.Logger),
		listen:        cfg.Listen,
		cacheSize:     cfg.CacheSizeBytes,
		cacheDisabled: cfg.CacheDisabled,
	}
	if len(r.listen) == 0 {
		r.listen = DefaultListen
	}
	if r.cacheSize <= 0 {
		r.cacheSize = DefaultCacheSizeBytes
	}

	upstreamAddrs := cfg.Upstreams
	if len(upstreamAddrs) == 0 {
		upstreamAddrs = []string{DefaultUpstream}
	}

	opts := &upstream.Options{
		Logger:  r.logger,
		Timeout: 10 * time.Second,
	}

	// Build one plain-DNS resolver per bootstrap address and combine them in
	// parallel, so dual-stack bootstrap lists work.
	var boots upstream.ParallelResolver
	for _, addr := range cfg.Bootstrap {
		boot, bErr := upstream.NewUpstreamResolver(addr, nil)
		if bErr != nil {
			_ = r.closeBootstrap()

			return nil, fmt.Errorf("creating bootstrap resolver %q: %w", addr, bErr)
		}
		r.bootstrap = append(r.bootstrap, boot)
		boots = append(boots, boot)
	}
	if len(boots) > 0 {
		opts.Bootstrap = boots
	}

	var ups []upstream.Upstream
	for _, addr := range upstreamAddrs {
		u, uErr := upstream.AddressToUpstream(addr, opts)
		if uErr != nil {
			closeUpstreams(ups)
			_ = r.closeBootstrap()

			return nil, fmt.Errorf("creating upstream %q: %w", addr, uErr)
		}
		ups = append(ups, u)
	}

	r.upstreamConf = &proxy.UpstreamConfig{Upstreams: ups}
	r.handler = &probeHandler{synthetic: SyntheticName()}

	return r, nil
}

// closeUpstreams closes a partially built upstream list.
func closeUpstreams(ups []upstream.Upstream) {
	for _, u := range ups {
		_ = u.Close()
	}
}

// SyntheticName returns a per-process random name that only this resolver's
// handler answers.  It proves a loopback endpoint is ours, not another process.
func (r *Resolver) SyntheticName() (name string) { return r.handler.synthetic }

// SyntheticName generates a fresh random probe name.
func SyntheticName() (name string) { return randomLabel() + ".dns-client-probe.invalid." }

// randomLabel returns a random 16-character hex label.
func randomLabel() (label string) {
	var b [8]byte
	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}

// Start binds the listeners and begins serving.  If the dual-stack listen set
// fails (e.g. IPv6 is disabled), it retries IPv4-only so enabling is not
// aborted by a ::1 bind failure.
func (r *Resolver) Start(ctx context.Context) (err error) {
	p, err := r.build(r.listen)
	if err == nil {
		err = p.Start(ctx)
	}
	if err != nil {
		if v4 := ipv4Addrs(r.listen); len(v4) != 0 && len(v4) != len(r.listen) {
			r.logger.WarnContext(ctx, "dual-stack bind failed, retrying IPv4-only", "err", err)

			var p4 *proxy.Proxy
			p4, err = r.build(v4)
			if err == nil {
				err = p4.Start(ctx)
			}
			if err == nil {
				r.proxy = p4

				return nil
			}
		}

		return fmt.Errorf(
			"binding DNS listeners on %v (another process may hold port 53, "+
				"e.g. Windows ICS/SharedAccess from Hyper-V/WSL/Docker): %w",
			r.listen, err,
		)
	}

	r.proxy = p

	return nil
}

// build constructs a Proxy for the given listen addresses, sharing upstreams.
func (r *Resolver) build(addrs []string) (p *proxy.Proxy, err error) {
	udp, tcp, err := parseAddrs(addrs)
	if err != nil {
		return nil, err
	}

	return proxy.New(&proxy.Config{
		Logger:         r.logger,
		UDPListenAddr:  udp,
		TCPListenAddr:  tcp,
		UpstreamConfig: r.upstreamConf,
		RequestHandler: r.handler,
		CacheEnabled:   !r.cacheDisabled,
		CacheSizeBytes: r.cacheSize,
	})
}

// parseAddrs resolves host:port strings into UDP and TCP listen addresses.
func parseAddrs(addrs []string) (udp []*net.UDPAddr, tcp []*net.TCPAddr, err error) {
	for _, a := range addrs {
		u, uErr := net.ResolveUDPAddr("udp", a)
		if uErr != nil {
			return nil, nil, fmt.Errorf("resolving UDP addr %q: %w", a, uErr)
		}
		t, tErr := net.ResolveTCPAddr("tcp", a)
		if tErr != nil {
			return nil, nil, fmt.Errorf("resolving TCP addr %q: %w", a, tErr)
		}

		udp = append(udp, u)
		tcp = append(tcp, t)
	}

	return udp, tcp, nil
}

// ipv4Addrs returns the subset of addrs whose host is an IPv4 address.
func ipv4Addrs(addrs []string) (v4 []string) {
	for _, a := range addrs {
		host, _, err := net.SplitHostPort(a)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			v4 = append(v4, a)
		}
	}

	return v4
}

// UDPAddrs returns the bound UDP listen addresses, or nil if not started.
func (r *Resolver) UDPAddrs() (addrs []string) {
	if r.proxy == nil {
		return nil
	}
	for _, a := range r.proxy.Addrs(proxy.ProtoUDP) {
		addrs = append(addrs, a.String())
	}

	return addrs
}

// ClearCache flushes the resolver cache.  Call on every state change.
func (r *Resolver) ClearCache() {
	if r.proxy != nil {
		r.proxy.ClearCache()
	}
}

// Shutdown stops the listeners and closes the upstreams.  The bootstrap
// resolvers' upstreams are closed separately since dnsproxy does not own them.
func (r *Resolver) Shutdown(ctx context.Context) (err error) {
	var errs []error
	if r.proxy != nil {
		errs = append(errs, r.proxy.Shutdown(ctx))
		r.proxy = nil
		r.upstreamConf = nil
	} else if r.upstreamConf != nil {
		errs = append(errs, r.upstreamConf.Close())
		r.upstreamConf = nil
	}
	errs = append(errs, r.closeBootstrap())

	return errors.Join(errs...)
}

// closeBootstrap closes the bootstrap upstreams if any were created.
func (r *Resolver) closeBootstrap() (err error) {
	errs := make([]error, 0, len(r.bootstrap))
	for _, b := range r.bootstrap {
		errs = append(errs, b.Close())
	}
	r.bootstrap = nil

	return errors.Join(errs...)
}

// probeHandler answers the synthetic probe name itself and forwards everything
// else to the default resolver via Proxy.Resolve.  Resolve must be called
// exactly once per request to avoid the double-forwarding trap; synthetic
// names short-circuit before it.
type probeHandler struct {
	synthetic string
}

// ServeDNS implements proxy.Handler.
func (h *probeHandler) ServeDNS(ctx context.Context, p *proxy.Proxy, dctx *proxy.DNSContext) (err error) {
	req := dctx.Req
	if len(req.Question) == 1 && strings.EqualFold(req.Question[0].Name, h.synthetic) {
		dctx.Res = probeResponse(req)

		return nil
	}

	return p.Resolve(ctx, dctx)
}

// probeResponse builds the answer for a synthetic-name query.
func probeResponse(req *dns.Msg) (resp *dns.Msg) {
	resp = &dns.Msg{}
	resp.SetReply(req)

	q := req.Question[0]
	hdr := dns.RR_Header{
		Name:   q.Name,
		Rrtype: q.Qtype,
		Class:  dns.ClassINET,
		Ttl:    0,
	}

	switch q.Qtype {
	case dns.TypeA:
		resp.Answer = append(resp.Answer, &dns.A{Hdr: hdr, A: net.ParseIP(probeIPv4).To4()})
	case dns.TypeAAAA:
		resp.Answer = append(resp.Answer, &dns.AAAA{Hdr: hdr, AAAA: net.ParseIP(probeIPv6)})
	}

	return resp
}

// loggerOrDefault returns l or a prefixed default logger.
func loggerOrDefault(l *slog.Logger) (logger *slog.Logger) {
	if l != nil {
		return l
	}

	return slog.Default().With("component", "resolver")
}
