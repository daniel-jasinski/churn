package analytics

import (
	"fmt"

	"churn/internal/domain"
	"churn/internal/event"
)

// Progress is one composite's §3.5 rollup: satisfied leaves over
// non-abandoned leaves across its whole subtree.
type Progress struct {
	Thing string
	// Satisfied counts satisfied leaves; Total counts non-abandoned leaves.
	Satisfied int
	Total     int
	// HasAbandoned is the §2.1 subtree flag.
	HasAbandoned bool
	// Display is "satisfied/total" — or "—" for the abandoned-only case,
	// which has no denominator: never a division by zero, never a silent
	// 100% (§3.5).
	Display string
}

// Fraction returns Satisfied/Total and ok=false for the abandoned-only
// no-denominator case (callers must not invent a number for it).
func (p Progress) Fraction() (float64, bool) {
	if p.Total == 0 {
		return 0, false
	}
	return float64(p.Satisfied) / float64(p.Total), true
}

// ProgressOf computes the rollup for one thing's subtree (leaves count
// themselves: a satisfied leaf is 1/1).
func ProgressOf(p *domain.Projection, thing string) Progress {
	pr := Progress{Thing: thing}
	for _, l := range p.Leaves(thing) {
		switch p.SemanticOf(p.Things[l]) {
		case event.SemanticAbandoned:
			pr.HasAbandoned = true
			continue // not in the denominator
		case event.SemanticSatisfied:
			pr.Satisfied++
		}
		pr.Total++
	}
	if pr.Total == 0 {
		pr.Display = "—"
	} else {
		pr.Display = fmt.Sprintf("%d/%d", pr.Satisfied, pr.Total)
	}
	return pr
}

// ProgressAll computes the rollup for every composite, sorted by thing id —
// progress bars at every level of the containment tree.
func ProgressAll(p *domain.Projection) []Progress {
	var out []Progress
	for _, id := range sortedIDs(p.Things) {
		if len(p.Things[id].Children) > 0 {
			out = append(out, ProgressOf(p, id))
		}
	}
	return out
}
