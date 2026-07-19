// Package match is the one matching engine behind readiness, resumable-now,
// contention, allocation validation, and the allocation proposer
// (DESIGN.md §2.4, §3.3). It computes maximum-cardinality bipartite matchings
// between requirement units (each requirement expanded to Quantity units) and
// free resource units (each resource expanded to its free unit count). The
// package is pure — no I/O, no clock, no dependency on the domain — and
// deterministic: results depend only on input values, never on input order.
//
// Eligibility: a resource unit can satisfy a requirement iff the resource
// carries ALL the requirement's capabilities (AND semantics), or is exactly
// the pinned resource.
//
// Tie-break (documented; every caller that displays assignments relies on
// it): requirements are processed in ascending id order, one unit at a time,
// and augmenting paths explore resources — and, when rerouting, current
// occupants — in ascending id order ("first eligible wins"). The maximum
// cardinality itself is unique; WHICH unit ends where is not, so
// per-requirement and per-resource attribution is indicative (§3.3): stable
// under the tie-break, but not the only optimum.
package match

import (
	"sort"
	"strings"
)

// Requirement is Quantity units of demand, eligible on resources carrying
// all Capabilities (AND), or on exactly the Pin. Exactly one of
// Capabilities/Pin is set by domain construction; a requirement with neither
// is eligible everywhere.
type Requirement struct {
	ID           string
	Quantity     int
	Capabilities []string
	Pin          string
}

// Signature is the requirement's contention signature (§3.3): the full
// AND-set (sorted, "+"-joined) or the pin ("pin:<resource id>").
func (r Requirement) Signature() string {
	if r.Pin != "" {
		return "pin:" + r.Pin
	}
	caps := append([]string(nil), r.Capabilities...)
	sort.Strings(caps)
	return strings.Join(caps, "+")
}

// Resource is a supply of Free interchangeable units carrying Capabilities.
type Resource struct {
	ID           string
	Free         int
	Capabilities map[string]struct{}
}

// Eligible reports whether units of r can satisfy req: r is the pinned
// resource, or carries every capability of the AND-set.
func Eligible(req Requirement, r Resource) bool {
	if req.Pin != "" {
		return r.ID == req.Pin
	}
	for _, c := range req.Capabilities {
		if _, ok := r.Capabilities[c]; !ok {
			return false
		}
	}
	return true
}

// Assigned is one cell of an assignment: Units units of Resource serving
// Requirement.
type Assigned struct {
	Requirement string
	Resource    string
	Units       int
}

// Result is the outcome of a maximum-cardinality matching. Matched and Unmet
// are authoritative; the per-requirement and per-resource attribution
// (Assignment, UnmetByRequirement, UsedByResource) depends on the tie-break
// and is indicative (§3.3) — deterministic, but not the only optimum.
type Result struct {
	// Demand is the total requirement units; Matched + Unmet == Demand.
	Demand  int
	Matched int
	Unmet   int
	// Assignment lists the non-zero cells, sorted by (requirement, resource).
	Assignment []Assigned
	// UnmetByRequirement maps requirement id → its unmet units (all
	// requirements present, including fully met ones).
	UnmetByRequirement map[string]int
	// UsedByResource maps resource id → units taken (all resources present).
	UsedByResource map[string]int
}

// Max computes a maximum-cardinality assignment of all requirement units
// onto distinct free resource units via augmenting paths (Kuhn's algorithm
// over unit-expanded requirements with per-resource unit counters — trivial
// at this scale). Deterministic under the package tie-break; input slices
// are not mutated.
func Max(reqs []Requirement, resources []Resource) Result {
	rs := append([]Requirement(nil), reqs...)
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	res := append([]Resource(nil), resources...)
	sort.Slice(res, func(i, j int) bool { return res[i].ID < res[j].ID })

	// adj[i] lists the eligible resource indexes of requirement i, ascending.
	adj := make([][]int, len(rs))
	for i, r := range rs {
		for j, u := range res {
			if u.Free > 0 && Eligible(r, u) {
				adj[i] = append(adj[i], j)
			}
		}
	}

	cnt := make([][]int, len(rs)) // cnt[i][j]: units of requirement i on resource j
	for i := range cnt {
		cnt[i] = make([]int, len(res))
	}
	used := make([]int, len(res))
	visited := make([]bool, len(res))

	// augment finds one augmenting path for a unit of requirement i: take a
	// free unit on the first eligible resource, or reroute one unit of a
	// current occupant (ascending requirement order) to make room.
	var augment func(i int) bool
	augment = func(i int) bool {
		for _, j := range adj[i] {
			if visited[j] {
				continue
			}
			visited[j] = true
			if used[j] < res[j].Free {
				used[j]++
				cnt[i][j]++
				return true
			}
			for i2 := range rs {
				if cnt[i2][j] > 0 && augment(i2) {
					cnt[i2][j]--
					cnt[i][j]++
					return true
				}
			}
		}
		return false
	}

	r := Result{
		UnmetByRequirement: make(map[string]int, len(rs)),
		UsedByResource:     make(map[string]int, len(res)),
	}
	for i := range rs {
		r.Demand += rs[i].Quantity
		for u := 0; u < rs[i].Quantity; u++ {
			for j := range visited {
				visited[j] = false
			}
			if augment(i) {
				r.Matched++
			}
		}
	}
	r.Unmet = r.Demand - r.Matched

	for i := range rs {
		met := 0
		for j := range res {
			if cnt[i][j] > 0 {
				r.Assignment = append(r.Assignment, Assigned{rs[i].ID, res[j].ID, cnt[i][j]})
				met += cnt[i][j]
			}
		}
		r.UnmetByRequirement[rs[i].ID] = rs[i].Quantity - met
	}
	for j := range res {
		r.UsedByResource[res[j].ID] = used[j]
	}
	return r
}

// Feasible reports whether ALL requirement units can be matched
// simultaneously onto distinct free units — the per-thing readiness question
// (§2.4) — returning the concrete assignment (the proposer's raw material)
// when they can. A nil requirement set is trivially feasible with an empty
// assignment.
func Feasible(reqs []Requirement, resources []Resource) ([]Assigned, bool) {
	r := Max(reqs, resources)
	if r.Unmet != 0 {
		return nil, false
	}
	return r.Assignment, true
}
