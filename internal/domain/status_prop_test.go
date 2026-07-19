package domain_test

import (
	"fmt"
	"testing"

	"churn/internal/domain"
	"churn/internal/domain/domaintest"
	"churn/internal/event"
)

// TestStatusProperties drives the domaintest generator and, after every
// batch, checks the derived-status layer against INDEPENDENT recomputations:
//
//   - statuses are disjoint and total over leaves — every leaf gets exactly
//     the one status the §2.2 table assigns it (semantic mirrors by
//     precedence; pending refined by independently recomputed dependency
//     satisfaction and brute-force matching feasibility);
//   - composite rollup matches the §2.1 closed table computed independently;
//   - the Statuses bookkeeping mirrors the freshly derived status for every
//     leaf (and only leaves) — the boundary refresh cannot lag;
//   - an active leaf with an uncovered requirement is impossible without the
//     out-of-step badge: M2/M3 validation enforces quantity-exact coverage
//     on every allocation-touching batch, so an UNBADGED uncovered
//     requirement cannot occur by construction (the reason there is no
//     dead uncovered-requirement badge to compute).
func TestStatusProperties(t *testing.T) {
	const batches = 250
	for _, seed := range []int64{3, 21} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			p, err := domain.Fold([]event.Envelope{fuzzInit()})
			if err != nil {
				t.Fatal(err)
			}
			g := domaintest.NewGenerator(seed)
			for i := 0; i < batches; i++ {
				cmds := g.Next(p)
				if cmds == nil {
					continue
				}
				evs := domaintest.Envelopes(p, fmt.Sprintf("b_%06d", i+1), cmds)
				if p, err = domain.ValidateBatch(p, evs, nil); err != nil {
					t.Fatalf("batch %d rejected: %v", i, err)
				}
				if err := checkStatusProperties(p); err != nil {
					t.Fatalf("after batch %d: %v", i, err)
				}
			}
		})
	}
}

func checkStatusProperties(p *domain.Projection) error {
	derived := p.DeriveAll()
	for id, th := range p.Things {
		d := derived[id]
		if len(th.Children) > 0 {
			if want := independentRollup(p, id); d.Status != want.Status || d.HasAbandoned != want.HasAbandoned {
				return fmt.Errorf("composite %s: derived %+v, independent %+v", id, d, want)
			}
			if _, ok := p.Statuses[id]; ok {
				return fmt.Errorf("composite %s carries a status entry", id)
			}
			continue
		}
		want, err := independentLeafStatus(p, id)
		if err != nil {
			return err
		}
		if d.Status != want {
			return fmt.Errorf("leaf %s: derived %q, independent %q", id, d.Status, want)
		}
		rec, ok := p.Statuses[id]
		if !ok {
			return fmt.Errorf("leaf %s has no status entry", id)
		}
		if rec.Status != d.Status {
			return fmt.Errorf("leaf %s: bookkeeping %q lags derived %q", id, rec.Status, d.Status)
		}
		// Uncovered-requirement-impossible: while active, an uncovered
		// requirement can only exist as legal §2.5 drift — always badged.
		if p.SemanticOf(th) == event.SemanticActive {
			covered := map[string]int{}
			for _, aid := range p.OpenAllocationsOf(id) {
				al := p.Allocations[aid]
				covered[al.Requirement] += al.Quantity
			}
			for rid, req := range p.Requirements {
				if req.Thing == id && covered[rid] != req.Quantity && !d.Badges.AllocationsOutOfStep {
					return fmt.Errorf("active leaf %s: requirement %s uncovered without out-of-step badge", id, rid)
				}
			}
		}
	}
	for id := range p.Statuses {
		if th, ok := p.Things[id]; !ok || len(th.Children) > 0 {
			return fmt.Errorf("stale status entry %s", id)
		}
	}
	return nil
}

// independentLeafStatus recomputes the §2.2 table without the production
// evaluator: direct semantic mapping, direct edge-satisfaction scan, and a
// backtracking brute-force matcher for feasibility.
func independentLeafStatus(p *domain.Projection, leaf string) (domain.Status, error) {
	switch p.SemanticOf(p.Things[leaf]) {
	case event.SemanticActive:
		return domain.StatusWorking, nil
	case event.SemanticSatisfied:
		return domain.StatusFinished, nil
	case event.SemanticPaused:
		return domain.StatusHeld, nil
	case event.SemanticAbandoned:
		return domain.StatusDropped, nil
	}
	for _, dep := range p.Dependencies {
		binds := false
		for _, l := range leavesOf(p, dep.From) {
			if l == leaf {
				binds = true
				break
			}
		}
		if !binds {
			continue
		}
		for _, l := range leavesOf(p, dep.To) {
			switch p.SemanticOf(p.Things[l]) {
			case event.SemanticSatisfied:
			case event.SemanticAbandoned:
				if dep.OnAbandoned == event.OnAbandonedBlock {
					return domain.StatusBlocked, nil
				}
			default:
				return domain.StatusBlocked, nil
			}
		}
	}
	if bruteFeasible(p, leaf) {
		return domain.StatusReady, nil
	}
	return domain.StatusResourceBlocked, nil
}

func leavesOf(p *domain.Projection, id string) []string {
	th, ok := p.Things[id]
	if !ok {
		return nil
	}
	if len(th.Children) == 0 {
		return []string{id}
	}
	var out []string
	for c := range th.Children {
		out = append(out, leavesOf(p, c)...)
	}
	return out
}

// bruteFeasible decides per-thing satisfiability by backtracking over the
// thing's requirements, unit by unit, against clamped free capacities —
// independent of the augmenting-path matcher.
func bruteFeasible(p *domain.Projection, thing string) bool {
	type unit struct {
		caps []string
		pin  string
	}
	var units []unit
	for _, req := range p.Requirements {
		if req.Thing != thing {
			continue
		}
		for i := 0; i < req.Quantity; i++ {
			units = append(units, unit{caps: req.Capabilities, pin: req.Resource})
		}
	}
	if len(units) == 0 {
		return true
	}
	free := map[string]int{}
	for id, rs := range p.Resources {
		f := rs.EffectiveCapacity() - p.AllocatedQuantity(id)
		if f > 0 {
			free[id] = f
		}
	}
	var solve func(k int) bool
	solve = func(k int) bool {
		if k == len(units) {
			return true
		}
		for id, f := range free {
			if f == 0 {
				continue
			}
			if units[k].pin != "" {
				if id != units[k].pin {
					continue
				}
			} else {
				ok := true
				for _, c := range units[k].caps {
					if _, has := p.Resources[id].Capabilities[c]; !has {
						ok = false
						break
					}
				}
				if !ok {
					continue
				}
			}
			free[id]--
			if solve(k + 1) {
				free[id]++
				return true
			}
			free[id]++
		}
		return false
	}
	return solve(0)
}

// independentRollup recomputes the §2.1 composite table row by row.
func independentRollup(p *domain.Projection, id string) domain.Derived {
	var terminal, active, paused, pending, abandoned int
	for _, l := range leavesOf(p, id) {
		switch p.SemanticOf(p.Things[l]) {
		case event.SemanticSatisfied:
			terminal++
		case event.SemanticAbandoned:
			terminal++
			abandoned++
		case event.SemanticActive:
			active++
		case event.SemanticPaused:
			paused++
		default:
			pending++
		}
	}
	d := domain.Derived{HasAbandoned: abandoned > 0}
	switch {
	case active == 0 && paused == 0 && pending == 0:
		d.Status = domain.StatusFinished
	case active > 0:
		d.Status = domain.StatusWorking
	case paused > 0 && pending == 0:
		d.Status = domain.StatusHeld
	default:
		d.Status = domain.StatusPending
	}
	return d
}
