// Package resolver owns the embedded dnsproxy Proxy: it binds the loopback
// listeners, carries the upstream configuration, and answers DNS over plain
// UDP/TCP on 127.0.0.1:53 and [::1]:53.
package resolver

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/daika7ana/dns-client/internal/filter"
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

// defaultListen is the dual-stack loopback listener set.
var defaultListen = []string{"127.0.0.1:53", "[::1]:53"}

// Block-response modes for filtering.
const (
	// BlockResponseZero answers blocked A/AAAA queries with 0.0.0.0 and ::.
	BlockResponseZero = "0.0.0.0"

	// BlockResponseNXDOMAIN answers blocked queries with NXDOMAIN.
	BlockResponseNXDOMAIN = "nxdomain"

	// DefaultBlockTTL is the default TTL for hand-built filtered responses.
	DefaultBlockTTL uint32 = 3600
)

// FilterConfig configures DNS filtering.
type FilterConfig struct {
	BlockResponse string
	Lists         []filter.List
	TTL           uint32
	Enabled       bool
}

// Rule is a per-domain routing rule (dnsmasq style).  Domains use the dnsmasq
// conventions: an empty string means unqualified (single-label) names only, a
// "*.sub.domain" entry matches subdomains but not the base domain, and an
// Upstream of "#" excludes the domains from any reserved upstream so they fall
// back to the default pool.
type Rule struct {
	Domains  []string
	Upstream string
}

// Config configures a Resolver.
type Config struct {
	Logger         *slog.Logger
	Upstreams      []string
	Bootstrap      []string
	Rules          []Rule
	Filter         FilterConfig
	Listen         []string
	CacheSizeBytes int
	CacheDisabled  bool
}

// filterState is the atomically-swappable filtering state.
type filterState struct {
	engine        *filter.Engine
	blockResponse string
	ttl           uint32
}

// Resolver is a running (or startable) loopback DNS resolver.
type Resolver struct {
	logger        *slog.Logger
	upstreamConf  *proxy.UpstreamConfig
	handler       *probeHandler
	proxy         *proxy.Proxy
	bootstrap     []*upstream.UpstreamResolver
	filterState   atomic.Pointer[filterState]
	filterCfg     FilterConfig
	listen        []string
	cacheSize     int
	cacheDisabled bool
}

// New builds a Resolver.  It creates the upstreams but does not bind anything
// until Start is called.
func New(cfg Config) (r *Resolver, err error) {
	r = &Resolver{
		logger:        loggerOrDefault(cfg.Logger),
		filterCfg:     cfg.Filter,
		listen:        cfg.Listen,
		cacheSize:     cfg.CacheSizeBytes,
		cacheDisabled: cfg.CacheDisabled,
	}
	if len(r.listen) == 0 {
		r.listen = defaultListen
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

	if len(cfg.Rules) > 0 {
		var rulesConf *proxy.UpstreamConfig
		rulesConf, err = parseRules(cfg.Rules, opts)
		if err != nil {
			closeUpstreams(ups)
			_ = r.closeBootstrap()

			return nil, err
		}

		// Rules are fixed for the resolver's lifetime; a rule change means a
		// restart, so there is no live cache to flush here.
		r.upstreamConf.DomainReservedUpstreams = rulesConf.DomainReservedUpstreams
		r.upstreamConf.SpecifiedDomainUpstreams = rulesConf.SpecifiedDomainUpstreams
		r.upstreamConf.SubdomainExclusions = rulesConf.SubdomainExclusions
	}

	r.handler = &probeHandler{synthetic: SyntheticName(), state: &r.filterState}
	if cfg.Filter.Enabled && len(cfg.Filter.Lists) > 0 {
		if err = r.reloadFilter(context.Background(), cfg.Filter); err != nil {
			closeUpstreams(ups)
			_ = r.closeBootstrap()

			return nil, err
		}
	}

	return r, nil
}

// RefreshLists re-downloads the configured filter lists, rebuilds the filter
// engine, and clears the resolver cache.  It is safe when filtering is
// disabled: reloadFilter then clears the filter state and the cache.
func (r *Resolver) RefreshLists(ctx context.Context) (err error) {
	return r.reloadFilter(ctx, r.filterCfg)
}

// reloadFilter loads the lists and swaps in a new filter state.
func (r *Resolver) reloadFilter(ctx context.Context, fc FilterConfig) (err error) {
	if !fc.Enabled || len(fc.Lists) == 0 {
		r.setFilterState(nil)
		r.clearCache()

		return nil
	}

	raw, err := filter.LoadAll(ctx, fc.Lists)
	if err != nil {
		return fmt.Errorf("loading filter lists: %w", err)
	}
	if len(raw) == 0 {
		r.setFilterState(nil)

		return nil
	}

	ttl := fc.TTL
	if ttl == 0 {
		ttl = DefaultBlockTTL
	}

	eng, err := filter.New(raw)
	if err != nil {
		return fmt.Errorf("building filter engine: %w", err)
	}

	r.setFilterState(&filterState{
		engine:        eng,
		blockResponse: fc.BlockResponse,
		ttl:           ttl,
	})
	r.clearCache()

	return nil
}

// setFilterState swaps in st, closing the previous engine.  Reload is expected
// to be called while idle; a request in flight may briefly use the old engine.
func (r *Resolver) setFilterState(st *filterState) {
	old := r.filterState.Swap(st)
	if old != nil && old.engine != nil {
		_ = old.engine.Close()
	}
}

// closeUpstreams closes a partially built upstream list.
func closeUpstreams(ups []upstream.Upstream) {
	for _, u := range ups {
		_ = u.Close()
	}
}

// parseRules converts structured rules into a proxy.UpstreamConfig holding only
// the per-domain upstreams.  The maps are built by dnsproxy's own dnsmasq
// parser (proxy.ParseUpstreamsConfig, exported in v0.86.0), so the matching
// semantics — more-specific-wins, //, *.sub.domain, and # exclusions — are
// exactly dnsproxy's.
func parseRules(rules []Rule, opts *upstream.Options) (conf *proxy.UpstreamConfig, err error) {
	lines, err := rulesToLines(rules)
	if err != nil {
		return nil, err
	}

	conf, err = proxy.ParseUpstreamsConfig(lines, opts)
	if err != nil {
		if conf != nil {
			_ = conf.Close()
		}

		return nil, fmt.Errorf("parsing per-domain rules: %w", err)
	}

	if len(conf.Upstreams) > 0 {
		// Every generated line carries a domain list, so this cannot happen;
		// fail loudly rather than silently add a default upstream.
		_ = conf.Close()

		return nil, errors.New("internal error: per-domain rule without domains")
	}

	return conf, nil
}

// rulesToLines renders structured rules in dnsmasq syntax:
// "[/domain1/domain2/]upstream".  An empty domain entry becomes the unqualified
// (//) form; a "#" upstream is an exclusion.
func rulesToLines(rules []Rule) (lines []string, err error) {
	for i, rule := range rules {
		if strings.TrimSpace(rule.Upstream) == "" {
			return nil, fmt.Errorf("rule %d: empty upstream", i)
		}
		if len(rule.Domains) == 0 {
			return nil, fmt.Errorf("rule %d: no domains", i)
		}

		domains := make([]string, len(rule.Domains))
		for j, d := range rule.Domains {
			if strings.Contains(d, "/") {
				return nil, fmt.Errorf("rule %d: domain %q contains %q", i, d, "/")
			}
			// Accept both "example.com" and "example.com.".
			domains[j] = strings.TrimSuffix(strings.TrimSpace(d), ".")
		}

		lines = append(lines, "[/"+strings.Join(domains, "/")+"/]"+rule.Upstream)
	}

	return lines, nil
}

// SyntheticName returns a per-process random name that only this resolver's
// handler answers.  It proves a loopback endpoint is ours, not another process.
func (r *Resolver) SyntheticName() (name string) { return r.handler.synthetic }

// SyntheticName generates a fresh random probe name.
func SyntheticName() (name string) { return rand.Text() + ".dns-client-probe.invalid." }

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
		// Ask validating upstreams for AD/RRSIGs; dns-client does not validate itself.
		DNSSECEnabled: true,
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
		ap, err := netip.ParseAddrPort(a)
		if err != nil {
			continue
		}
		if ap.Addr().Is4() {
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

// clearCache flushes the resolver cache.  Call on every state change.
func (r *Resolver) clearCache() {
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
	r.setFilterState(nil)

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

// probeHandler answers the synthetic probe name itself, applies DNS filtering,
// and forwards everything else to the default resolver via Proxy.Resolve.
// Order is fixed: synthetic short-circuit, then filter, then Resolve exactly
// once (filtering must run before Resolve, whose internal cache ignores a
// pre-set dctx.Res).
type probeHandler struct {
	synthetic string
	state     *atomic.Pointer[filterState]
}

// ServeDNS implements proxy.Handler.
func (h *probeHandler) ServeDNS(ctx context.Context, p *proxy.Proxy, dctx *proxy.DNSContext) (err error) {
	req := dctx.Req
	if len(req.Question) == 1 && strings.EqualFold(req.Question[0].Name, h.synthetic) {
		dctx.Res = probeResponse(req)

		return nil
	}

	if len(req.Question) == 1 {
		if st := h.state.Load(); st != nil && st.engine != nil {
			q := req.Question[0]
			if resp := buildFilterResponse(req, st.engine.Match(q.Name, q.Qtype), st); resp != nil {
				dctx.Res = resp

				return nil
			}
		}
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
