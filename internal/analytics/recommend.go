package analytics

import (
	"fmt"
	"sort"
	"time"

	"churn/internal/domain"
	"churn/internal/match"
)

// Settings are the §3.4 recommendation weights — live workspace settings,
// visible and adjustable; recommendations are advice given now, never
// persisted. The five terms mirror the spec formula:
//
//	score = w1·immediate_unlock + w2·downstream_reach + w3·remaining_depth
//	      + w4·waiting_age − w5·resource_scarcity_penalty
type Settings struct {
	// ImmediateUnlock (w1) weighs dependents made dependency-ready if this
	// finishes.
	ImmediateUnlock float64
	// DownstreamReach (w2) weighs everything transitively waiting on it.
	DownstreamReach float64
	// RemainingDepth (w3) weighs keeping the longest chain moving.
	RemainingDepth float64
	// WaitingAge (w4) weighs the starvation credit, per DAY of cumulative
	// resource-blocked time since the thing last held allocations (§3.4).
	WaitingAge float64
	// ScarcityPenalty (w5) weighs the matching-based pressure in [0,1] of
	// the thing's most contended signature.
	ScarcityPenalty float64
}

// DefaultSettings are the documented defaults: unlocking work now (w1=2)
// counts double a unit of transitive reach (w2=1) or chain depth (w3=1); a
// full day of starvation outweighs one unlocked dependent (w4=3/day); and a
// fully contended signature (pressure 1) costs about one unlocked dependent
// (w5=2) — a counterweighted nudge, not a veto, per §3.4.
func DefaultSettings() Settings {
	return Settings{
		ImmediateUnlock: 2,
		DownstreamReach: 1,
		RemainingDepth:  1,
		WaitingAge:      3,
		ScarcityPenalty: 2,
	}
}

// Term is one addend of a recommendation score, with its explanation — every
// score explains itself (§3.4).
type Term struct {
	// Name is the spec's term name (immediate_unlock, downstream_reach,
	// remaining_depth, waiting_age, resource_scarcity_penalty).
	Name string
	// Value is the raw measured value, Weight the setting applied,
	// Contribution = ±Weight·Value as summed into the score (negative for
	// the penalty).
	Value        float64
	Weight       float64
	Contribution float64
	// Detail is the human-readable disclosure, e.g. "unblocks 23" or
	// "waited 6d0h for contended capacity".
	Detail string
}

// Recommendation is one ready leaf's score with its full explanation.
type Recommendation struct {
	Thing string
	Score float64
	// Terms lists the five terms in the spec's formula order.
	Terms []Term
}

// Recommend ranks the ready leaves by the §3.4 score. Deterministic order:
// score descending, then thing id ascending.
func Recommend(p *domain.Projection, s Settings) []Recommendation {
	derived, leaves := leafStatuses(p)
	pressures := Contention(p).SignaturePressures()
	crit := newCritEval(p)

	var out []Recommendation
	for _, id := range leaves {
		if derived[id].Status != domain.StatusReady {
			continue
		}
		out = append(out, recommendOne(p, s, crit, pressures, id))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Thing < out[j].Thing
	})
	return out
}

func recommendOne(p *domain.Projection, s Settings, crit *critEval, pressures map[string]float64, id string) Recommendation {
	c := crit.of(id)
	starve := starvationOf(p, id)

	// waiting_age in days.
	age := starve.Credit.Hours() / 24

	// resource_scarcity_penalty: max matching-based pressure among the
	// thing's signatures — its bottleneck (§3.4).
	pressure, worstSig := 0.0, ""
	for _, r := range p.MatchRequirementsOf(id) {
		sig := r.Signature()
		if pr := pressures[sig]; worstSig == "" || pr > pressure {
			pressure, worstSig = pr, sig
		}
	}

	terms := []Term{
		{
			Name: "immediate_unlock", Value: float64(c.ImmediateUnlock), Weight: s.ImmediateUnlock,
			Contribution: s.ImmediateUnlock * float64(c.ImmediateUnlock),
			Detail:       fmt.Sprintf("unblocks %d if finished", c.ImmediateUnlock),
		},
		{
			Name: "downstream_reach", Value: float64(c.DownstreamReach), Weight: s.DownstreamReach,
			Contribution: s.DownstreamReach * float64(c.DownstreamReach),
			Detail:       fmt.Sprintf("%d transitively waiting on it", c.DownstreamReach),
		},
		{
			Name: "remaining_depth", Value: float64(c.RemainingDepth), Weight: s.RemainingDepth,
			Contribution: s.RemainingDepth * float64(c.RemainingDepth),
			Detail:       fmt.Sprintf("on a chain of %d unfinished steps", c.RemainingDepth),
		},
		{
			Name: "waiting_age", Value: age, Weight: s.WaitingAge,
			Contribution: s.WaitingAge * age,
			Detail:       waitingDetail(starve.Credit),
		},
		{
			Name: "resource_scarcity_penalty", Value: pressure, Weight: s.ScarcityPenalty,
			Contribution: -s.ScarcityPenalty * pressure,
			Detail:       scarcityDetail(worstSig, pressure),
		},
	}
	r := Recommendation{Thing: id, Terms: terms}
	for _, t := range terms {
		r.Score += t.Contribution
	}
	return r
}

func waitingDetail(credit time.Duration) string {
	if credit == 0 {
		return "no resource-blocked waiting on record"
	}
	d := credit / (24 * time.Hour)
	h := (credit % (24 * time.Hour)) / time.Hour
	return fmt.Sprintf("waited %dd%dh for capacity (credit retained across the ready flip)", d, h)
}

func scarcityDetail(sig string, pressure float64) string {
	if sig == "" {
		return "no resource requirements"
	}
	if pressure == 0 {
		return fmt.Sprintf("needs %s (uncontended)", sig)
	}
	return fmt.Sprintf("needs contended %s (pressure %.2f)", sig, pressure)
}

// ReadyEntry is one row of the §3.1 ready list: a ready leaf, its
// requirements, and its recommendation score with explanations.
type ReadyEntry struct {
	Thing   string
	Project string
	Type    string
	// Requirements are the leaf's requirements, sorted by requirement id.
	Requirements []match.Requirement
	// Score is the §3.4 recommendation for this leaf.
	Score Recommendation
}

// ReadyFilter narrows the ready list; zero values mean "no filter". Filters
// combine with AND.
type ReadyFilter struct {
	// Project keeps leaves of one project.
	Project string
	// Type keeps leaves of one thing type.
	Type string
	// Subtree keeps leaves inside the containment subtree of this thing.
	Subtree string
	// Capability keeps leaves with at least one requirement whose AND-set
	// contains this capability.
	Capability string
}

// Ready computes the §3.1 ready list: leaves with derived status ready,
// filtered, sorted like Recommend (score descending, then thing id) — the
// everyday screen.
func Ready(p *domain.Projection, s Settings, f ReadyFilter) []ReadyEntry {
	var subtree map[string]struct{}
	if f.Subtree != "" {
		if subtree = leafSet(p, f.Subtree); subtree == nil {
			return nil // unknown subtree filters everything out, not nothing
		}
	}
	var out []ReadyEntry
	for _, rec := range Recommend(p, s) {
		th := p.Things[rec.Thing]
		if f.Project != "" && th.Project != f.Project {
			continue
		}
		if f.Type != "" && th.Type != f.Type {
			continue
		}
		if subtree != nil {
			if _, in := subtree[rec.Thing]; !in {
				continue
			}
		}
		reqs := p.MatchRequirementsOf(rec.Thing)
		if f.Capability != "" && !anyRequires(reqs, f.Capability) {
			continue
		}
		out = append(out, ReadyEntry{
			Thing: rec.Thing, Project: th.Project, Type: th.Type,
			Requirements: reqs, Score: rec,
		})
	}
	return out
}

func anyRequires(reqs []match.Requirement, capability string) bool {
	for _, r := range reqs {
		for _, c := range r.Capabilities {
			if c == capability {
				return true
			}
		}
	}
	return false
}
