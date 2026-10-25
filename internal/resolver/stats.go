package resolver

import (
	"sync/atomic"
	"time"
)

// stats holds the live run counters for one Resolver.  They are per-resolver,
// not persisted: a restart or a config reload starts from zero.
type stats struct {
	queries     atomic.Uint64
	blocked     atomic.Uint64
	pausedUntil atomic.Int64 // UnixNano; 0 means filtering is not paused
}

// Stats is a point-in-time copy of the run counters.  It carries no query names
// or upstream data, so it is safe on the sanitized status pipe.
type Stats struct {
	// PausedUntil is zero unless filtering is paused and the pause is unexpired.
	PausedUntil time.Time

	Queries uint64
	Blocked uint64
}

// Stats returns the current counters.
func (r *Resolver) Stats() (s Stats) {
	s.Queries = r.stats.queries.Load()
	s.Blocked = r.stats.blocked.Load()

	if until := r.stats.pausedUntil.Load(); until != 0 {
		if t := time.Unix(0, until); t.After(time.Now()) {
			s.PausedUntil = t.UTC()
		}
	}

	return s
}

// PauseFiltering skips the filter stage (blocklists, hosts entries, and
// $dnsrewrite mappings) for d and clears the resolver cache, so an answer
// blocked just before the pause does not outlive it.  Queries still resolve
// through the upstreams: only the filter short-circuit is off.
func (r *Resolver) PauseFiltering(d time.Duration) {
	if d <= 0 {
		return
	}

	r.stats.pausedUntil.Store(time.Now().Add(d).UnixNano())
	r.ClearCache()
}

// ResumeFiltering turns the filter stage back on before the pause expires and
// clears the resolver cache, so an answer allowed during the pause does not
// outlive it.
func (r *Resolver) ResumeFiltering() {
	if r.stats.pausedUntil.Swap(0) != 0 {
		r.ClearCache()
	}
}

// paused reports whether the filter stage is currently bypassed.
func (s *stats) paused() (ok bool) {
	until := s.pausedUntil.Load()

	return until != 0 && time.Now().UnixNano() < until
}
