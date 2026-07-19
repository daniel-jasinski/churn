package analytics

import (
	"churn/internal/domain"
	"churn/internal/event"
)

// Criticality is the §3.3 structural-bottleneck triple for one thing. The
// three numbers are deliberately separate — transitive reach does not mean
// immediate unblocking. All three are measured over the expanded leaf graph
// (composite endpoints expanded per §2.1); for a composite thing they
// aggregate over its subtree's leaves.
type Criticality struct {
	Thing string
	// DownstreamReach counts the distinct leaves outside the thing's own
	// subtree that transitively depend on any of its leaves — everything
	// that can never finish without it. Structural: state-independent.
	DownstreamReach int
	// ImmediateUnlock counts the pending leaves that become
	// dependency-ready under a simulated completion of this thing
	// (== len(Unlocks)).
	ImmediateUnlock int
	// RemainingDepth is the longest chain of unfinished things (leaves in
	// non-terminal states) through this thing, in steps — the critical path
	// without durations. Counted in things, including this one; 0 when the
	// thing's own leaves are all terminal.
	RemainingDepth int
}

// CriticalityOf computes the triple for one thing.
func CriticalityOf(p *domain.Projection, thing string) Criticality {
	return newCritEval(p).of(thing)
}

// Criticalities computes the triple for every thing, sorted by thing id.
// (Dashboards re-sort by whichever number they rank on.)
func Criticalities(p *domain.Projection) []Criticality {
	e := newCritEval(p)
	out := make([]Criticality, 0, len(p.Things))
	for _, id := range sortedIDs(p.Things) {
		out = append(out, e.of(id))
	}
	return out
}

type critEval struct {
	p    *domain.Projection
	view *domain.DepView
	down map[string]int // memo: longest unfinished chain into dependencies, incl. self
	up   map[string]int // memo: longest unfinished chain into dependents, incl. self
}

func newCritEval(p *domain.Projection) *critEval {
	return &critEval{
		p: p, view: p.DepView(),
		down: map[string]int{}, up: map[string]int{},
	}
}

func (e *critEval) of(thing string) Criticality {
	own := leafSet(e.p, thing)
	c := Criticality{Thing: thing, ImmediateUnlock: len(unlocksWith(e.p, e.view, thing))}

	// Downstream reach: BFS over dependents from the thing's leaves.
	rev := e.view.Reverse()
	seen := map[string]struct{}{}
	var stack []string
	for _, l := range sortedSet(own) {
		stack = append(stack, rev[l]...)
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		stack = append(stack, rev[n]...)
	}
	for n := range seen {
		if _, internal := own[n]; !internal {
			c.DownstreamReach++
		}
	}

	// Remaining depth: for each unfinished own leaf, the longest unfinished
	// chain through it is up + down − 1 (the leaf counted once).
	for l := range own {
		if !e.unfinished(l) {
			continue
		}
		if d := e.chain(l, e.view.Reverse(), e.up) + e.chain(l, e.view.Graph(), e.down) - 1; d > c.RemainingDepth {
			c.RemainingDepth = d
		}
	}
	return c
}

// unfinished: a leaf in a non-terminal state — still to be worked, so it can
// sit on a remaining chain.
func (e *critEval) unfinished(leaf string) bool {
	switch e.p.SemanticOf(e.p.Things[leaf]) {
	case event.SemanticSatisfied, event.SemanticAbandoned:
		return false
	}
	return true
}

// chain returns the longest path of unfinished leaves starting at leaf
// (inclusive) along adj, memoized in memo. The expanded graph is acyclic
// (validated), so the recursion terminates.
func (e *critEval) chain(leaf string, adj map[string][]string, memo map[string]int) int {
	if !e.unfinished(leaf) {
		return 0
	}
	if d, ok := memo[leaf]; ok {
		return d
	}
	memo[leaf] = 1 // pre-set: cycles are impossible, this is just a base
	best := 1
	for _, n := range adj[leaf] {
		if d := 1 + e.chain(n, adj, memo); d > best {
			best = d
		}
	}
	memo[leaf] = best
	return best
}
