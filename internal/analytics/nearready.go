package analytics

import (
	"sort"

	"churn/internal/domain"
)

// DefaultNearReadyMax is the default frontier-size cutoff of NearReady:
// "almost ready" means at most this many blockers away.
const DefaultNearReadyMax = 2

// NearBlocker is one member of a near-ready thing's blocker frontier: the
// blocking entity and its own derived status, so the UI can render
// "waiting on X (in progress)". The status is the §2.2 leaf status or, for
// a declared composite target, the §2.1 rollup.
type NearBlocker struct {
	Thing  string
	Status domain.Status
}

// NearReadyEntry is one "almost ready" leaf: a pending-semantic leaf with
// derived status blocked whose minimal blocker frontier (§3.2) is small.
type NearReadyEntry struct {
	Thing   string
	Project string
	Type    string
	// Frontier is the transitively-reduced nearest-blocker frontier, sorted
	// by blocker id. Members are DECLARED dependency targets: an edge
	// pointing at a composite reports the composite (with its rollup
	// status), never its N subtree leaves — the blocker the user drew is the
	// blocker shown.
	Frontier []NearBlocker
	// Count is len(Frontier).
	Count int
}

// NearReady computes the "almost ready" companion of the §3.1 ready list:
// every leaf in a pending-semantic state with derived status blocked (NOT
// resource_blocked — those are already a ready-board column) whose minimal
// frontier of unfinished blockers (§3.2, transitively reduced via the
// BlockedBy machinery) has at most maxBlockers members. maxBlockers <= 0
// means DefaultNearReadyMax.
//
// Frontier members carry their own derived status, so a thing whose only
// blockers are themselves ready or resource_blocked is visible as one
// resource-freeing (or one start) away — no special casing. Composite
// blocker reporting: the frontier is reduced on the expanded leaf graph, but
// each surviving blocker is reported as the DECLARED target of the edge that
// contributed it — a composite target appears once, with its rollup status,
// rather than as its N leaves (and its leaf count never inflates the
// frontier size past a nearer declared blocker).
//
// The filter is the §3.1 ReadyFilter with identical semantics; capability
// filters by the blocked thing's OWN requirements, like ready entries.
// Deterministic order: frontier size ascending, then thing id.
func NearReady(p *domain.Projection, f ReadyFilter, maxBlockers int) []NearReadyEntry {
	if maxBlockers <= 0 {
		maxBlockers = DefaultNearReadyMax
	}
	var subtree map[string]struct{}
	if f.Subtree != "" {
		if subtree = leafSet(p, f.Subtree); subtree == nil {
			return nil // unknown subtree filters everything out, not nothing
		}
	}

	derived, leaves := leafStatuses(p)
	v := p.DepView()

	var out []NearReadyEntry
	for _, id := range leaves {
		if derived[id].Status != domain.StatusBlocked {
			continue
		}
		th := p.Things[id]
		if f.Project != "" && th.Project != f.Project {
			continue
		}
		if f.Type != "" && th.Type != f.Type {
			continue
		}
		if subtree != nil {
			if _, in := subtree[id]; !in {
				continue
			}
		}
		if f.Capability != "" && !anyRequires(p.MatchRequirementsOf(id), f.Capability) {
			continue
		}

		frontier := declaredFrontier(p, v, id)
		if len(frontier) == 0 || len(frontier) > maxBlockers {
			continue
		}
		entry := NearReadyEntry{
			Thing: id, Project: th.Project, Type: th.Type,
			Frontier: make([]NearBlocker, len(frontier)),
			Count:    len(frontier),
		}
		for i, b := range frontier {
			entry.Frontier[i] = NearBlocker{Thing: b, Status: derived[b].Status}
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count < out[j].Count
		}
		return out[i].Thing < out[j].Thing
	})
	return out
}

// declaredFrontier maps the leaf-level minimal frontier of §3.2 back to
// declared dependency targets: the sorted, deduped declared targets of the
// unsatisfied edges binding leaf whose blocker sets survived the transitive
// reduction. An edge whose every blocker was reduced away (a nearer blocker
// covers it) contributes nothing.
func declaredFrontier(p *domain.Projection, v *domain.DepView, leaf string) []string {
	minimal := map[string]struct{}{}
	for _, b := range blockedByWith(p, v, leaf).Frontier {
		minimal[b] = struct{}{}
	}
	declared := map[string]struct{}{}
	for _, did := range v.Binding()[leaf] {
		if v.Satisfied[did] {
			continue
		}
		for _, b := range v.EdgeBlockers(did) {
			if _, in := minimal[b]; in {
				declared[p.Dependencies[did].To] = struct{}{}
				break
			}
		}
	}
	return subsumeDeclared(p, sortedSet(declared))
}

// subsumeDeclared keeps the declared frontier minimal at the DECLARED level:
// a target whose expanded leaf set is contained in another surviving declared
// COMPOSITE target's subtree is dropped — an edge to WS plus an edge to a
// leaf inside WS is ONE blocker (WS), not two. Equal leaf sets keep the
// smaller id, deterministically.
func subsumeDeclared(p *domain.Projection, targets []string) []string {
	if len(targets) < 2 {
		return targets
	}
	sets := make(map[string]map[string]struct{}, len(targets))
	for _, t := range targets {
		sets[t] = leafSet(p, t)
	}
	keep := targets[:0]
	for _, t := range targets {
		subsumed := false
		for _, c := range targets {
			if c == t || len(p.Things[c].Children) == 0 {
				continue // only a composite target can subsume
			}
			if len(sets[t]) > len(sets[c]) {
				continue
			}
			if len(sets[t]) == len(sets[c]) && t < c {
				continue // equal sets: the smaller id survives
			}
			contained := true
			for l := range sets[t] {
				if _, in := sets[c][l]; !in {
					contained = false
					break
				}
			}
			if contained {
				subsumed = true
				break
			}
		}
		if !subsumed {
			keep = append(keep, t)
		}
	}
	return keep
}
