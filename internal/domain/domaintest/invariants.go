package domaintest

import (
	"fmt"

	"churn/internal/domain"
	"churn/internal/event"
)

// CheckInvariants asserts every global invariant of a validated projection.
// It is a deliberately INDEPENDENT implementation (e.g. cycle detection via
// Kahn's algorithm rather than the domain's DFS) so it can catch bugs in the
// production checks rather than mirror them.
func CheckInvariants(p *domain.Projection) error {
	// Containment is a forest: parents exist, links are mutual, projects
	// agree, and walking up terminates.
	for id, th := range p.Things {
		if th.Parent != "" {
			pt, ok := p.Things[th.Parent]
			if !ok {
				return fmt.Errorf("thing %s has dangling parent %s", id, th.Parent)
			}
			if _, ok := pt.Children[id]; !ok {
				return fmt.Errorf("thing %s not in parent %s child set", id, th.Parent)
			}
			if pt.Project != th.Project {
				return fmt.Errorf("thing %s parented across projects (%s under %s)", id, th.Project, pt.Project)
			}
		}
		for c := range th.Children {
			ct, ok := p.Things[c]
			if !ok {
				return fmt.Errorf("thing %s has dangling child %s", id, c)
			}
			if ct.Parent != id {
				return fmt.Errorf("child %s of %s points at parent %q", c, id, ct.Parent)
			}
		}
		steps := 0
		for a := th.Parent; a != ""; a = p.Things[a].Parent {
			if steps++; steps > len(p.Things) {
				return fmt.Errorf("containment cycle above thing %s", id)
			}
		}
		if _, ok := p.Projects[th.Project]; !ok {
			return fmt.Errorf("thing %s in dangling project %s", id, th.Project)
		}
		if _, ok := p.Types[th.Type]; !ok {
			return fmt.Errorf("thing %s has dangling type %s", id, th.Type)
		}
		if th.State != "" {
			if _, ok := p.States[th.State]; !ok {
				return fmt.Errorf("thing %s in dangling state %s", id, th.State)
			}
		}
		// Composites are stateless and requirement-free.
		if len(th.Children) > 0 {
			if th.State != "" {
				return fmt.Errorf("composite %s carries state %s", id, th.State)
			}
			for rid, req := range p.Requirements {
				if req.Thing == id {
					return fmt.Errorf("composite %s carries requirement %s", id, rid)
				}
			}
		}
	}

	// Dependencies reference live things.
	for id, dep := range p.Dependencies {
		if _, ok := p.Things[dep.From]; !ok {
			return fmt.Errorf("dependency %s has dangling from %s", id, dep.From)
		}
		if _, ok := p.Things[dep.To]; !ok {
			return fmt.Errorf("dependency %s has dangling to %s", id, dep.To)
		}
		if dep.OnAbandoned != event.OnAbandonedBlock && dep.OnAbandoned != event.OnAbandonedIgnore {
			return fmt.Errorf("dependency %s has unnormalized policy %q", id, dep.OnAbandoned)
		}
	}

	// The expanded leaf graph is acyclic — Kahn's algorithm.
	if err := expandedAcyclic(p); err != nil {
		return err
	}

	// Requirements: owned by a live leaf, forms valid, references live.
	for id, req := range p.Requirements {
		th, ok := p.Things[req.Thing]
		if !ok {
			return fmt.Errorf("requirement %s has dangling thing %s", id, req.Thing)
		}
		if len(th.Children) > 0 {
			return fmt.Errorf("requirement %s on composite %s", id, req.Thing)
		}
		if (req.Resource != "") == (len(req.Capabilities) > 0) {
			return fmt.Errorf("requirement %s has invalid form", id)
		}
		if req.Resource != "" {
			rs, ok := p.Resources[req.Resource]
			if !ok {
				return fmt.Errorf("requirement %s pins dangling resource %s", id, req.Resource)
			}
			if !rs.Named {
				return fmt.Errorf("requirement %s pins unnamed resource %s", id, req.Resource)
			}
			if req.Quantity != 1 {
				return fmt.Errorf("pinned requirement %s has quantity %d", id, req.Quantity)
			}
		}
		for _, c := range req.Capabilities {
			if _, ok := p.Capabilities[c]; !ok {
				return fmt.Errorf("requirement %s references dangling capability %s", id, c)
			}
		}
		if req.Quantity < 1 {
			return fmt.Errorf("requirement %s has quantity %d", id, req.Quantity)
		}
	}

	// Resources: named ⇒ capacity 1; capability grants are declared.
	for id, rs := range p.Resources {
		if rs.Named && rs.Capacity != 1 {
			return fmt.Errorf("named resource %s has capacity %d", id, rs.Capacity)
		}
		if rs.Capacity < 1 {
			return fmt.Errorf("resource %s has capacity %d", id, rs.Capacity)
		}
		for c := range rs.Capabilities {
			if _, ok := p.Capabilities[c]; !ok {
				return fmt.Errorf("resource %s granted dangling capability %s", id, c)
			}
		}
	}

	// Open allocations: on an active leaf, referencing a live requirement of
	// that thing and a live resource. Capacity accounting is consistent:
	// free capacity clamps at max(0, effective − allocated), never negative
	// arithmetic downstream.
	for id, al := range p.Allocations {
		if al.Quantity < 1 {
			return fmt.Errorf("allocation %s has quantity %d", id, al.Quantity)
		}
		if !al.Open {
			continue // closed allocations are history; their refs may be gone
		}
		th, ok := p.Things[al.Thing]
		if !ok {
			return fmt.Errorf("open allocation %s has dangling thing %s", id, al.Thing)
		}
		if len(th.Children) > 0 {
			return fmt.Errorf("open allocation %s on composite %s", id, al.Thing)
		}
		if st, ok := p.States[th.State]; !ok || st.Semantic != event.SemanticActive {
			return fmt.Errorf("open allocation %s on non-active thing %s (state %q)", id, al.Thing, th.State)
		}
		req, ok := p.Requirements[al.Requirement]
		if !ok {
			return fmt.Errorf("open allocation %s references dangling requirement %s", id, al.Requirement)
		}
		if req.Thing != al.Thing {
			return fmt.Errorf("open allocation %s references foreign requirement %s", id, al.Requirement)
		}
		if _, ok := p.Resources[al.Resource]; !ok {
			return fmt.Errorf("open allocation %s references dangling resource %s", id, al.Resource)
		}
	}

	// Versions: every live entity has a version entry.
	for _, ids := range [][]string{
		sortedKeys(p.States), sortedKeys(p.Types), sortedKeys(p.Capabilities),
		sortedKeys(p.Projects), sortedKeys(p.Things), sortedKeys(p.Dependencies),
		sortedKeys(p.Requirements), sortedKeys(p.Resources), sortedKeys(p.Allocations),
	} {
		for _, id := range ids {
			if p.Versions[id] <= 0 {
				return fmt.Errorf("entity %s has no version", id)
			}
			if p.Versions[id] > p.LastSeq {
				return fmt.Errorf("entity %s version %d beyond LastSeq %d", id, p.Versions[id], p.LastSeq)
			}
		}
	}
	return nil
}

// expandedAcyclic verifies the expanded leaf graph has no cycle, using
// Kahn's algorithm (independent of the domain's DFS).
func expandedAcyclic(p *domain.Projection) error {
	leavesOf := func(id string) []string {
		var out []string
		var walk func(string)
		walk = func(n string) {
			th, ok := p.Things[n]
			if !ok {
				return
			}
			if len(th.Children) == 0 {
				out = append(out, n)
				return
			}
			for c := range th.Children {
				walk(c)
			}
		}
		walk(id)
		return out
	}

	adj := map[string]map[string]struct{}{}
	indeg := map[string]int{}
	for _, dep := range p.Dependencies {
		for _, f := range leavesOf(dep.From) {
			for _, t := range leavesOf(dep.To) {
				if adj[f] == nil {
					adj[f] = map[string]struct{}{}
				}
				if _, dup := adj[f][t]; !dup {
					adj[f][t] = struct{}{}
					indeg[t]++
				}
			}
		}
	}
	var queue []string
	nodes := map[string]struct{}{}
	for f, ts := range adj {
		nodes[f] = struct{}{}
		for t := range ts {
			nodes[t] = struct{}{}
		}
	}
	for n := range nodes {
		if indeg[n] == 0 {
			queue = append(queue, n)
		}
	}
	processed := 0
	for len(queue) > 0 {
		n := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		processed++
		for t := range adj[n] {
			if indeg[t]--; indeg[t] == 0 {
				queue = append(queue, t)
			}
		}
	}
	if processed != len(nodes) {
		return fmt.Errorf("expanded leaf graph has a cycle (%d of %d nodes acyclic)", processed, len(nodes))
	}
	return nil
}
