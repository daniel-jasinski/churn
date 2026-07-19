package main

import (
	"strings"
	"testing"
	"time"

	"churn/internal/analytics"
	"churn/internal/domain"
	"churn/internal/event"
	"churn/internal/store"
)

// foldWorkspace replays a data directory's log into a projection, the same
// way a fresh serve would.
func foldWorkspace(t *testing.T, dir string) *domain.Projection {
	t.Helper()
	st, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var evs []event.Envelope
	if err := st.Scan(func(ev event.Envelope) error {
		evs = append(evs, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err := domain.Fold(evs)
	if err != nil {
		t.Fatalf("seeded log does not fold: %v", err)
	}
	return p
}

// thingByName resolves the one thing with the given display name.
func thingByName(t *testing.T, p *domain.Projection, name string) string {
	t.Helper()
	found := ""
	for id, th := range p.Things {
		if th.Name == name {
			if found != "" {
				t.Fatalf("duplicate thing name %q", name)
			}
			found = id
		}
	}
	if found == "" {
		t.Fatalf("no thing named %q", name)
	}
	return found
}

func TestSeedDemo(t *testing.T) {
	dir := t.TempDir()
	out, _, err := runCLI(t, "seed-demo", "--data", dir)
	if err != nil {
		t.Fatalf("seed-demo: %v", err)
	}
	if !strings.Contains(out, "seeded into") {
		t.Fatalf("output = %q", out)
	}

	// Refuses a non-empty directory — the demo never lands on real data.
	if _, _, err := runCLI(t, "seed-demo", "--data", dir); err == nil ||
		!strings.Contains(err.Error(), "not empty") {
		t.Fatalf("second seed-demo: err = %v, want 'not empty'", err)
	}

	p := foldWorkspace(t, dir)

	// Fixture shape.
	if got := len(p.Projects); got != 2 {
		t.Errorf("projects = %d, want 2", got)
	}
	if got := len(p.Things); got != 32 {
		t.Errorf("things = %d, want 32", got)
	}
	if got := len(p.Resources); got != 6 {
		t.Errorf("resources = %d, want 6", got)
	}

	// Ready list: exactly the nine leaves the scenario leaves startable.
	ready := analytics.Ready(p, analytics.DefaultSettings(), analytics.ReadyFilter{})
	if got := len(ready); got != 9 {
		names := make([]string, len(ready))
		for i, r := range ready {
			names[i] = p.Things[r.Thing].Name
		}
		t.Errorf("ready = %d %v, want 9", got, names)
	}

	// Near-ready: the almost-ready companion of the ready list is populated.
	// Four blocked leaves are one blocker away — the print run (behind the
	// abandoned vendor comparison, policy block), the findings (behind the
	// running analysis), and the blog post and archive (behind the findings)
	// — while the final content review (four blockers) stays beyond the
	// default cutoff.
	nearEntries := analytics.NearReady(p, analytics.ReadyFilter{}, 0)
	nearByName := map[string]analytics.NearReadyEntry{}
	for _, en := range nearEntries {
		nearByName[p.Things[en.Thing].Name] = en
	}
	if len(nearEntries) != 4 {
		names := make([]string, 0, len(nearByName))
		for n := range nearByName {
			names = append(names, n)
		}
		t.Errorf("near-ready = %d %v, want 4", len(nearEntries), names)
	}
	vendor := thingByName(t, p, "Print vendor comparison")
	if en, ok := nearByName["Order print run"]; !ok || en.Count != 1 ||
		en.Frontier[0].Thing != vendor || en.Frontier[0].Status != domain.StatusDropped {
		t.Errorf("print run near-ready = %+v, want frontier [vendor dropped]", en)
	}
	if en, ok := nearByName["Publish pilot findings"]; !ok || en.Count != 1 ||
		en.Frontier[0].Status != domain.StatusWorking {
		t.Errorf("findings near-ready = %+v, want frontier [analyze working]", en)
	}
	if _, ok := nearByName["Final content review"]; ok {
		t.Error("final review (4 blockers) must be beyond the default near-ready cutoff")
	}

	// Contention: the §3.3 flashing light — six ready things wanting the
	// review signature against marginal capacity two — plus nonzero total.
	rep := analytics.Contention(p)
	if rep.Unmet < 4 {
		t.Errorf("contention unmet = %d, want >= 4", rep.Unmet)
	}
	foundFlashing := false
	for _, sig := range rep.Signatures {
		if sig.Demand == 6 && sig.Unmet == 4 && len(sig.Things) == 6 {
			foundFlashing = true
		}
	}
	if !foundFlashing {
		t.Errorf("no review signature with demand 6 / unmet 4 in %+v", rep.Signatures)
	}

	// Out-of-step: the analysis requirement was superseded while active.
	analyze := thingByName(t, p, "Analyze pilot data")
	if d := p.Derive(analyze); d.Status != domain.StatusWorking || !d.Badges.AllocationsOutOfStep {
		t.Errorf("analyze derived = %+v, want working + out-of-step badge", d)
	}

	// Paused and resumable: Maria freed up when the field guide was paused.
	guide := thingByName(t, p, "Draft field guide")
	if d := p.Derive(guide); d.Status != domain.StatusHeld || !d.ResumableNow {
		t.Errorf("guide derived = %+v, want held + resumable-now", d)
	}

	// The abandoned vendor comparison under both §2.2 edge policies.
	printrun := thingByName(t, p, "Order print run")
	if d := p.Derive(printrun); d.Status != domain.StatusBlocked {
		t.Errorf("print run status = %v, want blocked (policy block)", d.Status)
	}
	site := thingByName(t, p, "Publish launch site")
	if d := p.Derive(site); d.Status != domain.StatusReady || !d.Badges.AbandonedDependency {
		t.Errorf("site derived = %+v, want ready + abandoned-dependency badge", d)
	}

	// Starvation: the copyedit (greedy-vs-matching case) has been
	// resource_blocked for days of fixture time; the workshop's stint began
	// when facilitation capacity hit zero.
	starve := map[string]analytics.Starvation{}
	for _, s := range analytics.Starvations(p) {
		starve[p.Things[s.Thing].Name] = s
	}
	if s, ok := starve["Copyedit & sign-off brochure"]; !ok || s.CurrentStint < 9*24*time.Hour {
		t.Errorf("copyedit starvation = %+v, want current stint >= 9d", s)
	}
	if s, ok := starve["Plan stakeholder workshop"]; !ok || s.CurrentStint < 2*24*time.Hour {
		t.Errorf("workshop starvation = %+v, want current stint >= 2d", s)
	}

	// The pinned requirement made it into the fixture (§2.4).
	pinned := false
	for _, req := range p.Requirements {
		if req.Pinned() {
			pinned = true
		}
	}
	if !pinned {
		t.Error("no pinned requirement in the fixture")
	}
}
