package analytics

import (
	"sort"

	"churn/internal/domain"
	"churn/internal/event"
)

// Blocked explains what keeps a thing from being dependency-satisfied
// (§3.2): the minimal frontier of nearest unfinished blockers, plus the full
// blocker graph for expanding chains.
type Blocked struct {
	Thing string
	// Frontier is the minimal frontier: the nearest unfinished blockers —
	// leaves directly preventing an edge of the thing's leaves from being
	// satisfied — after transitive reduction (no member is reachable from
	// another member in the blocker graph). Sorted by id.
	Frontier []string
	// Chains is the full blocker graph, for expanding a frontier member to
	// its own blockers and so on down the chain: every transitively reached
	// blocker → its sorted direct blockers (a nil entry marks a chain end,
	// e.g. an unfinished leaf whose own dependencies are satisfied).
	Chains map[string][]string
}

// directBlockers returns the sorted leaves directly preventing the edges
// binding leaf from being satisfied.
func directBlockers(v *domain.DepView, leaf string) []string {
	set := map[string]struct{}{}
	for _, did := range v.Binding()[leaf] {
		for _, b := range v.EdgeBlockers(did) {
			set[b] = struct{}{}
		}
	}
	return sortedSet(set)
}

// BlockedBy computes the §3.2 explanation for one thing (leaf or composite —
// a composite is explained through its leaves, ignoring blockers inside its
// own subtree: those are its progress, not its dependencies).
func BlockedBy(p *domain.Projection, thing string) Blocked {
	return blockedByWith(p, p.DepView(), thing)
}

// blockedByWith is BlockedBy over a shared DepView (one view serves a whole
// sweep, e.g. NearReady).
func blockedByWith(p *domain.Projection, v *domain.DepView, thing string) Blocked {
	own := leafSet(p, thing)

	frontier := map[string]struct{}{}
	for _, l := range sortedSet(own) {
		for _, b := range directBlockers(v, l) {
			if _, internal := own[b]; !internal {
				frontier[b] = struct{}{}
			}
		}
	}

	// Expand the full blocker graph from the frontier.
	chains := map[string][]string{}
	queue := sortedSet(frontier)
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if _, done := chains[n]; done {
			continue
		}
		next := directBlockers(v, n)
		chains[n] = next
		queue = append(queue, next...)
	}

	// Transitive reduction of the frontier: drop members reachable from
	// another member through the blocker graph.
	reachable := func(from, to string) bool {
		seen := map[string]struct{}{}
		stack := append([]string(nil), chains[from]...)
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n == to {
				return true
			}
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			stack = append(stack, chains[n]...)
		}
		return false
	}
	var minimal []string
	for _, u := range sortedSet(frontier) {
		redundant := false
		for w := range frontier {
			if w != u && reachable(w, u) {
				redundant = true
				break
			}
		}
		if !redundant {
			minimal = append(minimal, u)
		}
	}

	return Blocked{Thing: thing, Frontier: minimal, Chains: chains}
}

// Unlocks is the inverse view of §3.2 and the immediate-unlock counter of
// §3.3: the pending-semantic leaves, currently NOT dependency-satisfied,
// that become dependency-satisfied under a simulated completion of thing.
// Simulated completion finishes the subtree's non-terminal leaves; its
// ALREADY-ABANDONED leaves stay abandoned, so a dependent behind an
// on_abandoned=block edge is never claimed unlockable — finishing the rest
// of the subtree can never unblock it. Resource feasibility is deliberately
// not consulted — "dependency-ready" is the honest claim (§3.3). Sorted by
// id.
func Unlocks(p *domain.Projection, thing string) []string {
	return unlocksWith(p, p.DepView(), thing)
}

// unlocksWith is Unlocks over a shared DepView (one view serves a whole
// sweep). Only direct dependents of the thing's leaves in the expanded
// graph are examined — no other leaf's dependency satisfaction can change
// under the simulated completion.
func unlocksWith(p *domain.Projection, v *domain.DepView, thing string) []string {
	ownLeaves := p.Leaves(thing)
	if ownLeaves == nil {
		return nil
	}
	own := make(map[string]struct{}, len(ownLeaves))
	assume := make(map[string]struct{}, len(ownLeaves))
	for _, l := range ownLeaves {
		own[l] = struct{}{}
		if p.SemanticOf(p.Things[l]) != event.SemanticAbandoned {
			assume[l] = struct{}{}
		}
	}
	cands := map[string]struct{}{}
	rev := v.Reverse()
	for _, l := range ownLeaves {
		for _, d := range rev[l] {
			cands[d] = struct{}{}
		}
	}
	var out []string
	for _, id := range sortedSet(cands) {
		if _, internal := own[id]; internal {
			continue
		}
		if p.SemanticOf(p.Things[id]) != event.SemanticPending {
			continue
		}
		if ok, _ := v.DepsSatisfied(id); ok {
			continue // already dependency-ready
		}
		if v.DepsSatisfiedAssuming(id, assume) {
			out = append(out, id)
		}
	}
	return out
}

func sortedIDs[V any](m map[string]V) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortedSet(s map[string]struct{}) []string {
	return sortedIDs(s)
}
