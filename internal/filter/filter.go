// Package filter is a thin wrapper around the urlfilter DNS engine (SPEC §6).
//
// It is deliberately free of dnsproxy: it turns raw filter-list text into a
// match result that the resolver turns into a DNS response.  Filtering defaults
// to off; callers only build an Engine when filtering is enabled.
package filter

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/AdguardTeam/urlfilter"
	"github.com/AdguardTeam/urlfilter/filterlist"
	"github.com/AdguardTeam/urlfilter/rules"
	"github.com/miekg/dns"
)

// ErrNoLists is returned by New when no raw lists are provided.
var ErrNoLists = errors.New("no filter lists provided")

// Action is the outcome of a filter match.
type Action int

// Match actions.
const (
	// ActionNone means no rule matched; forward the request.
	ActionNone Action = iota

	// ActionBlock means a blocking rule matched.
	ActionBlock

	// ActionAllow means an exception (@@) rule matched; forward the request.
	ActionAllow

	// ActionRewrite means a $dnsrewrite rule matched.
	ActionRewrite

	// ActionHosts means a hosts-style rule matched; answer from the rule's IP.
	ActionHosts
)

// Rewrite describes a matched $dnsrewrite rule.
type Rewrite struct {
	// Value is the rewritten record value: netip.Addr for A/AAAA, string for
	// PTR/TXT, and urlfilter-specific types for the rest.
	Value any

	// NewCNAME is the target of a CNAME rewrite, if set.
	NewCNAME string

	// RCode is the rewritten DNS response code.
	RCode int

	// RRType is the rewritten record type (only if RCode is RcodeSuccess).
	RRType uint16
}

// Result is the outcome of Engine.Match.
type Result struct {
	// Rewrite is set when Action is ActionRewrite.
	Rewrite *Rewrite

	// Hosts are the IPs from matching hosts rules (ActionHosts).
	Hosts []netip.Addr

	// Action is the match outcome.
	Action Action
}

// RawList is one filter list already read into memory.
type RawList struct {
	// ID is a human-readable list identifier (used for diagnostics only).
	ID string

	// Text is the raw list contents.  It must not be modified while the Engine
	// built from it is alive.
	Text []byte
}

// Engine is a compiled filtering engine.
type Engine struct {
	eng     *urlfilter.DNSEngine
	storage *filterlist.RuleStorage
}

// New builds an Engine from raw list contents.  Each list is parsed with
// cosmetic rules ignored (they are meaningless for DNS filtering).
func New(lists []RawList) (e *Engine, err error) {
	if len(lists) == 0 {
		return nil, ErrNoLists
	}

	boxes := make([]filterlist.Interface, 0, len(lists))
	for i, l := range lists {
		// Every list needs a unique ListID or NewRuleStorage fails; the index
		// is unique within this build.
		boxes = append(boxes, filterlist.NewBytes(&filterlist.BytesConfig{
			ID:             rules.ListID(i + 1),
			RulesText:      l.Text,
			IgnoreCosmetic: true,
		}))
	}

	storage, err := filterlist.NewRuleStorage(boxes)
	if err != nil {
		return nil, fmt.Errorf("creating rule storage: %w", err)
	}

	return &Engine{
		eng:     urlfilter.NewDNSEngine(storage),
		storage: storage,
	}, nil
}

// RulesCount returns the number of rules loaded into the engine.
func (e *Engine) RulesCount() (n uint64) {
	if e == nil || e.eng == nil {
		return 0
	}

	return e.eng.RulesCount()
}

// Match runs the filter for host and qtype.  host may be given with or without a
// trailing dot and in any case.
func (e *Engine) Match(host string, qtype uint16) (r Result) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return r
	}

	dres, matched := e.eng.MatchRequest(&urlfilter.DNSRequest{
		Hostname: host,
		DNSType:  qtype,
	})

	// DNS rewrites MUST be checked before matched: matched is false when only a
	// $dnsrewrite (or other special) rule matched.
	if rewrites := dres.DNSRewrites(); len(rewrites) > 0 {
		rw := rewrites[0].DNSRewrite
		r.Action = ActionRewrite
		r.Rewrite = &Rewrite{
			Value:    rw.Value,
			NewCNAME: rw.NewCNAME,
			RCode:    rw.RCode,
			RRType:   rw.RRType,
		}

		return r
	}

	if matched && dres.NetworkRule != nil {
		if dres.NetworkRule.Whitelist {
			r.Action = ActionAllow
		} else {
			r.Action = ActionBlock
		}

		return r
	}

	// Hosts rules are address-family specific.
	switch {
	case qtype == dns.TypeA && len(dres.HostRulesV4) > 0:
		r.Action = ActionHosts
		for _, h := range dres.HostRulesV4 {
			r.Hosts = append(r.Hosts, h.IP)
		}
	case qtype == dns.TypeAAAA && len(dres.HostRulesV6) > 0:
		r.Action = ActionHosts
		for _, h := range dres.HostRulesV6 {
			r.Hosts = append(r.Hosts, h.IP)
		}
	default:
		r.Action = ActionNone
	}

	return r
}

// Close releases the rule storage.
func (e *Engine) Close() (err error) {
	if e == nil || e.storage == nil {
		return nil
	}

	return e.storage.Close()
}
