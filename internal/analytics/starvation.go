package analytics

import (
	"sort"
	"time"

	"churn/internal/domain"
)

// Starvation is one leaf's §3.3 starvation record, read from the
// projection's batch-boundary bookkeeping — a pure function of the log,
// with "now" being the last batch commit timestamp.
type Starvation struct {
	Thing string
	// CurrentStint is the uninterrupted resource_blocked stint so far: from
	// the status-entry timestamp to the projection's LastTS. Zero unless
	// the leaf is currently resource_blocked.
	CurrentStint time.Duration
	// Credit is the cumulative resource-blocked time since the thing last
	// held allocations (§3.4): completed stints plus the current one. It is
	// retained across the flip to ready — the waiting_age term of the
	// recommendation score reads exactly this number.
	Credit time.Duration
}

// Starvations lists every non-terminal leaf with a non-zero current stint
// or retained credit, sorted by CurrentStint descending (the dashboard
// highlights the current stint), then Credit descending, then thing id.
// Finished and dropped leaves are excluded — their retained credit can
// never matter again, so listing them forever would be noise — while the
// underlying bookkeeping (ThingStatus.BlockedFor) is left untouched.
func Starvations(p *domain.Projection) []Starvation {
	var out []Starvation
	for _, id := range sortedIDs(p.Statuses) {
		switch p.Statuses[id].Status {
		case domain.StatusFinished, domain.StatusDropped:
			continue
		}
		s := starvationOf(p, id)
		if s.CurrentStint > 0 || s.Credit > 0 {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.CurrentStint != b.CurrentStint {
			return a.CurrentStint > b.CurrentStint
		}
		if a.Credit != b.Credit {
			return a.Credit > b.Credit
		}
		return a.Thing < b.Thing
	})
	return out
}

// starvationOf reads one leaf's record; the zero Starvation for unknown ids.
func starvationOf(p *domain.Projection, thing string) Starvation {
	rec, ok := p.Statuses[thing]
	if !ok {
		return Starvation{Thing: thing}
	}
	s := Starvation{Thing: thing, Credit: rec.BlockedFor}
	if rec.Status == domain.StatusResourceBlocked {
		s.CurrentStint = domain.TSDelta(rec.SinceTS, p.LastTS)
		s.Credit += s.CurrentStint
	}
	return s
}
