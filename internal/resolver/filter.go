package resolver

import (
	"net"
	"net/netip"

	"github.com/daika7ana/sigil-dns/internal/filter"
	"github.com/miekg/dns"
)

// buildFilterResponse turns a filter match into a response, or nil to forward.
// ActionNone and ActionAllow both forward (an allow is an exception rule).
func buildFilterResponse(req *dns.Msg, res filter.Result, st *filterState) (resp *dns.Msg) {
	switch res.Action {
	case filter.ActionBlock:
		return blockResponse(req, st.blockResponse, st.ttl)
	case filter.ActionHosts:
		return hostsResponse(req, res.Hosts, st.ttl)
	case filter.ActionRewrite:
		return rewriteResponse(req, res.Rewrites, st.ttl)
	default:
		return nil
	}
}

// newReply builds a reply with RA set and the request's EDNS0 size and DO bit
// echoed.  Hand-built responses are not cached, so flags are set explicitly.
func newReply(req *dns.Msg) (resp *dns.Msg) {
	resp = &dns.Msg{}
	resp.SetReply(req)
	resp.RecursionAvailable = true

	if o := req.IsEdns0(); o != nil {
		resp.SetEdns0(o.UDPSize(), o.Do())
	}

	return resp
}

// rrHdr builds an RR header with an explicit TTL.
func rrHdr(name string, rrtype uint16, ttl uint32) (h dns.RR_Header) {
	return dns.RR_Header{Name: name, Rrtype: rrtype, Class: dns.ClassINET, Ttl: ttl}
}

// blockResponse builds a blocked response: 0.0.0.0 / :: by default, or NXDOMAIN.
func blockResponse(req *dns.Msg, mode string, ttl uint32) (resp *dns.Msg) {
	resp = newReply(req)

	if mode == BlockResponseNXDOMAIN {
		resp.Rcode = dns.RcodeNameError

		return resp
	}

	q := req.Question[0]
	switch q.Qtype {
	case dns.TypeA:
		resp.Answer = []dns.RR{&dns.A{Hdr: rrHdr(q.Name, dns.TypeA, ttl), A: net.IPv4zero.To4()}}
	case dns.TypeAAAA:
		resp.Answer = []dns.RR{&dns.AAAA{Hdr: rrHdr(q.Name, dns.TypeAAAA, ttl), AAAA: net.IPv6zero}}
	}

	return resp
}

// hostsResponse answers from hosts-rule IPs matching the query family.  A
// matched hosts rule with no address for this type is still a block: answer
// empty NOERROR locally instead of forwarding (e.g. 0.0.0.0 for an AAAA or
// HTTPS query).
func hostsResponse(req *dns.Msg, hosts []netip.Addr, ttl uint32) (resp *dns.Msg) {
	resp = newReply(req)
	q := req.Question[0]

	for _, ip := range hosts {
		switch {
		case q.Qtype == dns.TypeA && ip.Is4():
			resp.Answer = append(resp.Answer,
				&dns.A{Hdr: rrHdr(q.Name, dns.TypeA, ttl), A: ip.AsSlice()})
		case q.Qtype == dns.TypeAAAA && ip.Is6() && !ip.Is4In6():
			resp.Answer = append(resp.Answer,
				&dns.AAAA{Hdr: rrHdr(q.Name, dns.TypeAAAA, ttl), AAAA: ip.AsSlice()})
		}
	}

	return resp
}

// rewriteResponse applies every matching $dnsrewrite rule, answering only with
// records for the question's type.  A rewrite with no record for the query type
// still suppresses forwarding: the response is an empty NOERROR.
func rewriteResponse(req *dns.Msg, rws []filter.Rewrite, ttl uint32) (resp *dns.Msg) {
	resp = newReply(req)
	resp.Compress = true

	q := req.Question[0]

	// A CNAME rewrite redirects the whole name, so it takes precedence over
	// address rewrites.
	for _, rw := range rws {
		if rw.NewCNAME != "" {
			resp.Answer = []dns.RR{&dns.CNAME{
				Hdr:    rrHdr(q.Name, dns.TypeCNAME, ttl),
				Target: dns.Fqdn(rw.NewCNAME),
			}}

			return resp
		}
	}

	for _, rw := range rws {
		if rw.RCode != dns.RcodeSuccess {
			resp.Rcode = rw.RCode

			return resp
		}
	}

	for _, rw := range rws {
		switch {
		case q.Qtype == dns.TypeA && rw.RRType == dns.TypeA:
			if ip, ok := rw.Value.(netip.Addr); ok && ip.Is4() {
				resp.Answer = append(resp.Answer,
					&dns.A{Hdr: rrHdr(q.Name, dns.TypeA, ttl), A: ip.AsSlice()})
			}
		case q.Qtype == dns.TypeAAAA && rw.RRType == dns.TypeAAAA:
			if ip, ok := rw.Value.(netip.Addr); ok && ip.Is6() {
				resp.Answer = append(resp.Answer,
					&dns.AAAA{Hdr: rrHdr(q.Name, dns.TypeAAAA, ttl), AAAA: ip.AsSlice()})
			}
		}
	}

	return resp
}
