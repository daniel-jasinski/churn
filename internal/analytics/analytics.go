// Package analytics implements DESIGN.md §3 as pure functions over the
// domain projection: ready-work discovery, blocked-by explanation,
// criticality, resource contention, starvation, the next-work
// recommendation score, and the progress rollup.
//
// Ground rules the whole package obeys:
//
//   - Pure: no I/O, no clock. "Now" is the projection's last batch commit
//     timestamp (Projection.LastTS) — all analytics are answers as of the
//     last committed batch, which is also what makes them time-travelable
//     (§3.6).
//   - One brain: everything reads the projection and the domain's derived
//     layer; resource questions all go through the one matching engine
//     (internal/match) that also defines readiness and validates
//     allocations.
//   - Deterministic: every list is sorted on a documented key; matching
//     attribution follows the match package's documented tie-break.
//   - Honest numbers: matching-based totals are authoritative; attribution
//     splits are labeled indicative, naive per-tag ratios heuristic (§3.3).
package analytics

import (
	"sort"

	"churn/internal/domain"
)

// leafStatuses derives every leaf's status once, returning the sorted leaf
// ids alongside for deterministic iteration.
func leafStatuses(p *domain.Projection) (map[string]domain.Derived, []string) {
	derived := p.DeriveAll()
	leaves := make([]string, 0, len(p.Things))
	for id, th := range p.Things {
		if len(th.Children) == 0 {
			leaves = append(leaves, id)
		}
	}
	sort.Strings(leaves)
	return derived, leaves
}

// leafSet returns the leaves of thing's subtree as a set (thing itself if a
// leaf); nil for unknown ids.
func leafSet(p *domain.Projection, thing string) map[string]struct{} {
	ls := p.Leaves(thing)
	if ls == nil {
		return nil
	}
	set := make(map[string]struct{}, len(ls))
	for _, l := range ls {
		set[l] = struct{}{}
	}
	return set
}
