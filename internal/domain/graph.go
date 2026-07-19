package domain

// ExpandedLeafGraph returns the expanded leaf dependency adjacency (§2.1):
// for every leaf with outgoing dependencies, the sorted, de-duplicated
// leaves it depends on — every composite endpoint expanded to its subtree's
// leaves, inherited edges included. This is the one graph all leaf-level
// analytics (criticality, blocked-by chains) are measured over.
func (p *Projection) ExpandedLeafGraph() map[string][]string {
	adj := p.expandedAdjacency()
	out := make(map[string][]string, len(adj))
	for f, ts := range adj {
		out[f] = sortedKeys(ts)
	}
	return out
}

// expandedAdjacency builds the expanded leaf graph as sets.
func (p *Projection) expandedAdjacency() map[string]map[string]struct{} {
	// Scale is hundreds of things (DESIGN.md), so the
	// leaves(from) × leaves(to) expansion is cheap.
	adj := map[string]map[string]struct{}{}
	for _, id := range sortedKeys(p.Dependencies) {
		dep := p.Dependencies[id]
		toLeaves := p.Leaves(dep.To)
		for _, f := range p.Leaves(dep.From) {
			edges := adj[f]
			if edges == nil {
				edges = map[string]struct{}{}
				adj[f] = edges
			}
			for _, t := range toLeaves {
				edges[t] = struct{}{}
			}
		}
	}
	return adj
}

// expandedCycle checks acyclicity on the EXPANDED LEAF GRAPH (§2.1): every
// composite endpoint of a declared dependency is expanded to its subtree's
// leaves — an edge originating at a composite is inherited by all leaves of
// the subtree, and an edge targeting a composite makes each of those leaves
// depend on every leaf of the target subtree ("all leaves terminal"). This
// catches declared-acyclic but effectively cyclic constraints, e.g. a leaf
// depending on its own ancestor's subtree: expansion makes it depend on
// itself.
//
// It returns nil if the expanded graph is acyclic, otherwise one offending
// cycle as an ordered list of leaf thing ids [t0, t1, …, tn] meaning
// t0 → t1 → … → tn → t0 (a self-dependency is the one-element cycle [t0]).
// Detection is deterministic: nodes and neighbors are visited in sorted
// order, so the same projection always reports the same cycle.
func (p *Projection) expandedCycle() []string {
	adj := p.expandedAdjacency()

	const (
		white = iota // unvisited
		gray         // on the current DFS path
		black        // fully explored, cycle-free
	)
	color := make(map[string]int, len(adj))
	var stack []string

	var visit func(n string) []string
	visit = func(n string) []string {
		color[n] = gray
		stack = append(stack, n)
		for _, m := range sortedKeys(adj[n]) {
			switch color[m] {
			case gray:
				// Back edge: the cycle is the stack suffix from m onward.
				for i, s := range stack {
					if s == m {
						return append([]string(nil), stack[i:]...)
					}
				}
			case white:
				if c := visit(m); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return nil
	}

	for _, n := range sortedKeys(adj) {
		if color[n] == white {
			if c := visit(n); c != nil {
				return c
			}
		}
	}
	return nil
}
