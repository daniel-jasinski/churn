package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"churn/internal/event"
	"churn/internal/writer"
)

// cmdSeedDemo creates a realistic demo workspace in an empty data directory.
// Everything goes through the writer — the same validate → commit → publish
// path as live edits, never raw SQL — so the demo log satisfies every §5.2
// invariant and survives export → import byte-identically.
//
// The fixture is built to make each screen show something worth looking at:
// two projects, containment with a §2.1 final-review child step, a
// cross-project dependency, an abandoned thing behind both on_abandoned
// policies, a pinned requirement, active things holding allocations, a
// paused (resumable) thing, an out-of-step active thing (§2.5), the §3.3
// flashing-light contention scenario (six ready things wanting a signature
// with marginal capacity two), and starvation stints. Events are stamped
// over the three weeks before "now" (the writer's clock is injected), so
// stint durations and history read like a lived-in workspace.
func cmdSeedDemo(args []string, stdout, stderr io.Writer) error {
	fs2, data := newFlagSet("seed-demo", "seed-demo --data <dir>", stderr)
	if err := fs2.Parse(args); err != nil {
		return err
	}
	dir, err := requireData(*data)
	if err != nil {
		return err
	}
	// A demo must never land on top of real data: only a missing or empty
	// directory is accepted (same posture as import-log).
	if entries, err := os.ReadDir(dir); err == nil {
		if len(entries) > 0 {
			return fmt.Errorf("seed-demo: %s is not empty — seed-demo creates a fresh demo workspace only", dir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	st, err := openWorkspace(dir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	clk := &demoClock{t: time.Now().UTC().Add(-21 * 24 * time.Hour)}
	w, err := writer.Open(st, writer.Options{Actor: "daniel", Now: clk.now})
	if err != nil {
		return err
	}
	defer w.Close()

	if err := seedDemo(w, clk); err != nil {
		return fmt.Errorf("seed-demo: %w", err)
	}

	p := w.Projection()
	fmt.Fprintf(stdout, "churn: demo workspace %s seeded into %s\n", p.WorkspaceID, dir)
	fmt.Fprintf(stdout, "churn: %d projects, %d things, %d resources, %d events\n",
		len(p.Projects), len(p.Things), len(p.Resources), p.LastSeq)
	fmt.Fprintf(stdout, "churn: explore it: churn serve --data %s\n", dir)
	return nil
}

// demoClock is the injected writer clock: a settable wall clock the seeder
// advances between batches, so the demo's history spans days instead of
// milliseconds. Reads and writes are serialized by the writer's own
// request/reply channels (advance only runs between Submit calls).
type demoClock struct{ t time.Time }

func (c *demoClock) now() time.Time          { return c.t }
func (c *demoClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// seeder accumulates the demo through the writer with a sticky error: after
// the first failure every later step is a no-op, so the happy path reads as
// the scenario script it is. Minted ids are tracked under short human keys.
type seeder struct {
	w   *writer.Writer
	clk *demoClock
	ids map[string]string
	err error
}

// mint mints a fresh id under key and returns it.
func (s *seeder) mint(prefix, key string) string {
	if s.err != nil {
		return ""
	}
	id, err := s.w.MintID(prefix)
	if err != nil {
		s.err = err
		return ""
	}
	s.ids[key] = id
	return id
}

// id resolves a previously minted key; a typo is a seeder bug and fails the
// whole seed rather than emitting a half-built fixture.
func (s *seeder) id(key string) string {
	if s.err != nil {
		return ""
	}
	id, ok := s.ids[key]
	if !ok {
		s.err = fmt.Errorf("seed: unknown key %q", key)
		return ""
	}
	return id
}

// submit sends one batch through the writer.
func (s *seeder) submit(actor string, cmds ...writer.Command) {
	if s.err != nil {
		return
	}
	if _, err := s.w.Submit(actor, cmds, nil); err != nil {
		s.err = err
	}
}

// pass advances the demo clock, so the next batch commits later.
func (s *seeder) pass(d time.Duration) {
	if s.err == nil {
		s.clk.advance(d)
	}
}

// ── command constructors (thin sugar over the event catalog) ──

func (s *seeder) defType(key, name string) writer.Command {
	return writer.Command{Type: event.TypeTypeDefined, V: 1, Entity: s.mint(event.PrefixType, key),
		Payload: event.TypeDefined{Name: name}}
}

func (s *seeder) defCap(key, name string) writer.Command {
	return writer.Command{Type: event.TypeCapabilityDefined, V: 1, Entity: s.mint(event.PrefixCapability, key),
		Payload: event.CapabilityDefined{Name: name}}
}

func (s *seeder) project(key, name string) writer.Command {
	return writer.Command{Type: event.TypeProjectCreated, V: 1, Entity: s.mint(event.PrefixProject, key),
		Payload: event.ProjectCreated{Name: name}}
}

func (s *seeder) resource(key, name string, named bool, capacity int) writer.Command {
	return writer.Command{Type: event.TypeResourceCreated, V: 1, Entity: s.mint(event.PrefixResource, key),
		Payload: event.ResourceCreated{Name: name, Kind: event.KindReusable, Named: named, Capacity: capacity}}
}

func (s *seeder) grant(resKey, capKey string) writer.Command {
	return writer.Command{Type: event.TypeCapabilityGranted, V: 1, Entity: s.id(resKey),
		Payload: event.CapabilityGranted{Capability: s.id(capKey)}}
}

// thing creates a thing; parentKey "" means a root.
func (s *seeder) thing(key, projectKey, name, typeKey, parentKey string) writer.Command {
	parent := ""
	if parentKey != "" {
		parent = s.id(parentKey)
	}
	return writer.Command{Type: event.TypeThingCreated, V: 1, Entity: s.mint(event.PrefixThing, key),
		Payload: event.ThingCreated{Project: s.id(projectKey), Name: name, Type: s.id(typeKey), Parent: parent}}
}

// dep asserts fromKey depends on toKey; policy "" means the default (ignore).
func (s *seeder) dep(key, fromKey, toKey, policy string) writer.Command {
	return writer.Command{Type: event.TypeDependencyAsserted, V: 1, Entity: s.mint(event.PrefixDependency, key),
		Payload: event.DependencyAsserted{From: s.id(fromKey), To: s.id(toKey), OnAbandoned: policy}}
}

// reqCaps asserts a capability requirement on a thing (key "req-" + thingKey
// unless the thing carries several).
func (s *seeder) reqCaps(key, thingKey string, quantity int, capKeys ...string) writer.Command {
	caps := make([]string, len(capKeys))
	for i, ck := range capKeys {
		caps[i] = s.id(ck)
	}
	return writer.Command{Type: event.TypeRequirementAsserted, V: 1, Entity: s.mint(event.PrefixRequirement, key),
		Payload: event.RequirementAsserted{Thing: s.id(thingKey), Quantity: quantity, Capabilities: caps}}
}

// reqPin asserts a pinned requirement (§2.4; quantity is 1 by rule).
func (s *seeder) reqPin(key, thingKey, resKey string) writer.Command {
	return writer.Command{Type: event.TypeRequirementAsserted, V: 1, Entity: s.mint(event.PrefixRequirement, key),
		Payload: event.RequirementAsserted{Thing: s.id(thingKey), Quantity: 1, Resource: s.id(resKey)}}
}

// state transitions a thing into the named default state.
func (s *seeder) state(thingKey, stateKey string) writer.Command {
	return writer.Command{Type: event.TypeThingStateChanged, V: 1, Entity: s.id(thingKey),
		Payload: event.ThingStateChanged{State: s.id(stateKey)}}
}

// alloc opens an allocation satisfying reqKey of thingKey from resKey.
func (s *seeder) alloc(key, thingKey, resKey, reqKey string, quantity int) writer.Command {
	return writer.Command{Type: event.TypeAllocationOpened, V: 1, Entity: s.mint(event.PrefixAllocation, key),
		Payload: event.AllocationOpened{Thing: s.id(thingKey), Resource: s.id(resKey),
			Quantity: quantity, Requirement: s.id(reqKey)}}
}

func (s *seeder) closeAlloc(key string) writer.Command {
	return writer.Command{Type: event.TypeAllocationClosed, V: 1, Entity: s.id(key),
		Payload: event.AllocationClosed{}}
}

// seedDemo writes the demo scenario. The timeline starts three weeks ago
// (the writer.Open batch) and ends two days ago, so stints and waiting ages
// are visibly nonzero when the workspace is first opened.
func seedDemo(w *writer.Writer, clk *demoClock) error {
	s := &seeder{w: w, clk: clk, ids: map[string]string{}}

	// The §2.2 default states were seeded by writer.Open; index them by name.
	p := w.Projection()
	for id, st := range p.States {
		s.ids["st-"+st.Name] = id
	}

	// Day 0: vocabulary, projects, resources.
	s.pass(1 * time.Hour)
	s.submit("daniel",
		s.defType("ty-task", "task"),
		s.defType("ty-review", "review"),
		s.defType("ty-deliverable", "deliverable"),
		s.defCap("cap-editing", "editing"),
		s.defCap("cap-approval", "approval"),
		s.defCap("cap-review", "review"),
		s.defCap("cap-data", "data-analysis"),
		s.defCap("cap-facilitation", "facilitation"),
	)
	s.pass(1 * time.Hour)
	s.submit("daniel",
		s.project("launch", "Aurora Product Launch"),
		s.project("study", "Field Study 2026"),
	)
	s.pass(1 * time.Hour)
	s.submit("daniel",
		// One fungible pool; the rest are named per the §2.3 guidance.
		s.resource("reviewers", "Reviewers", false, 3),
		s.resource("maria", "Maria K.", true, 1),
		s.resource("jonas-r", "Jonas B.", true, 1),
		s.resource("labrig", "Lab Rig A", true, 1),
		s.resource("room", "Workshop Room", true, 1),
		s.resource("priya-r", "Priya S.", true, 1),
		s.grant("reviewers", "cap-review"),
		// Maria is the demo's multi-capability unit: the sole carrier of
		// both editing and approval, which makes greedy-vs-matching visible
		// (one unit cannot satisfy two requirements at once, §2.4).
		s.grant("maria", "cap-editing"),
		s.grant("maria", "cap-approval"),
		s.grant("jonas-r", "cap-data"),
		s.grant("labrig", "cap-data"),
		s.grant("room", "cap-facilitation"),
		s.grant("priya-r", "cap-facilitation"),
	)

	// Day 1: the launch project, entered as bulk batches (the /batch shape).
	// "Launch content" carries a final-review child step depending on its
	// sibling tasks — the §2.1 pattern for work belonging to the composite.
	s.pass(21 * time.Hour)
	s.submit("ana",
		s.thing("content", "launch", "Launch content", "ty-task", ""),
		s.thing("msg", "launch", "Draft launch messaging", "ty-task", "content"),
		s.thing("pricing", "launch", "Draft pricing page", "ty-task", "content"),
		s.thing("faq", "launch", "Write FAQ", "ty-task", "content"),
		s.thing("email", "launch", "Draft onboarding email", "ty-task", "content"),
		s.thing("rev-pricing", "launch", "Review pricing page", "ty-review", "content"),
		s.thing("rev-faq", "launch", "Review FAQ", "ty-review", "content"),
		s.thing("rev-email", "launch", "Review onboarding email", "ty-review", "content"),
		s.thing("legal", "launch", "Incorporate legal feedback", "ty-task", "content"),
		s.thing("final", "launch", "Final content review", "ty-review", "content"),
		s.dep("d1", "rev-pricing", "pricing", ""),
		s.dep("d2", "rev-faq", "faq", ""),
		s.dep("d3", "rev-email", "email", ""),
		s.dep("d4", "legal", "msg", ""),
		s.dep("d5", "final", "rev-pricing", ""),
		s.dep("d6", "final", "rev-faq", ""),
		s.dep("d7", "final", "rev-email", ""),
		s.dep("d8", "final", "legal", ""),
		s.reqCaps("req-rev-pricing", "rev-pricing", 1, "cap-review"),
		s.reqCaps("req-rev-faq", "rev-faq", 1, "cap-review"),
		s.reqCaps("req-rev-email", "rev-email", 1, "cap-review"),
		s.reqCaps("req-legal", "legal", 1, "cap-review"),
		s.reqCaps("req-final", "final", 1, "cap-review"),
	)
	s.pass(2 * time.Hour)
	s.submit("ana",
		s.thing("print", "launch", "Print campaign", "ty-task", ""),
		s.thing("vendor", "launch", "Print vendor comparison", "ty-task", "print"),
		s.thing("printrun", "launch", "Order print run", "ty-task", "print"),
		s.thing("site", "launch", "Publish launch site", "ty-deliverable", "print"),
		s.thing("brochure", "launch", "Design brochure", "ty-deliverable", "print"),
		s.thing("copyedit", "launch", "Copyedit & sign-off brochure", "ty-review", "print"),
		// The abandoned-dependency pair (§2.2): the same target under both
		// policies — block keeps the print run stuck, ignore lets the site
		// ship with the warning badge.
		s.dep("d9", "printrun", "vendor", event.OnAbandonedBlock),
		s.dep("d10", "site", "vendor", event.OnAbandonedIgnore),
		s.dep("d11", "copyedit", "brochure", ""),
		s.reqCaps("req-site", "site", 1, "cap-review"),
		// Two requirements, both only satisfiable by Maria: individually
		// each looks coverable (the greedy read), together they are not —
		// the matching keeps this honestly resource_blocked (§2.4).
		s.reqCaps("req-copyedit-ed", "copyedit", 1, "cap-editing"),
		s.reqCaps("req-copyedit-ap", "copyedit", 1, "cap-approval"),
	)
	s.pass(2 * time.Hour)
	s.submit("ana",
		s.thing("swag", "launch", "Order launch swag", "ty-task", ""),
		s.thing("blog", "launch", "Write launch blog post", "ty-deliverable", ""),
		s.thing("workshop", "launch", "Plan stakeholder workshop", "ty-task", ""),
		s.thing("presskit", "launch", "Review press kit", "ty-review", ""),
		s.thing("demoscript", "launch", "Review demo script", "ty-review", ""),
		s.reqCaps("req-workshop", "workshop", 1, "cap-facilitation"),
		s.reqCaps("req-presskit", "presskit", 1, "cap-review"),
		s.reqCaps("req-demoscript", "demoscript", 1, "cap-review"),
	)

	// Day 2: the study project, plus the cross-project edge (§2.1 "rarely").
	s.pass(20 * time.Hour)
	s.submit("jonas",
		s.thing("pilot", "study", "Pilot phase", "ty-task", ""),
		s.thing("protocol", "study", "Design study protocol", "ty-task", "pilot"),
		s.thing("recruit", "study", "Recruit pilot participants", "ty-task", "pilot"),
		s.thing("sessions", "study", "Run pilot sessions", "ty-task", "pilot"),
		s.thing("onboard", "study", "Facilitate pilot onboarding", "ty-task", "pilot"),
		s.thing("analyze", "study", "Analyze pilot data", "ty-task", "pilot"),
		s.thing("findings", "study", "Publish pilot findings", "ty-deliverable", "pilot"),
		s.thing("guide", "study", "Draft field guide", "ty-deliverable", ""),
		s.thing("bench", "study", "Run bench calibration", "ty-task", ""),
		s.thing("ethics", "study", "Ethics re-approval", "ty-task", ""),
		s.thing("archive", "study", "Archive raw recordings", "ty-task", ""),
		s.dep("d12", "recruit", "protocol", ""),
		s.dep("d13", "sessions", "recruit", ""),
		s.dep("d14", "onboard", "recruit", ""),
		s.dep("d15", "analyze", "sessions", ""),
		s.dep("d16", "findings", "analyze", ""),
		s.dep("d17", "archive", "findings", ""),
		s.reqCaps("req-onboard", "onboard", 1, "cap-facilitation"),
		s.reqCaps("req-analyze", "analyze", 1, "cap-data"),
		s.reqCaps("req-guide", "guide", 1, "cap-editing"),
		s.reqPin("req-bench", "bench", "labrig"),
		s.reqCaps("req-ethics", "ethics", 1, "cap-approval"),
	)
	s.pass(2 * time.Hour)
	s.submit("ana", s.dep("d18", "blog", "findings", ""))

	// Days 3–9: work happens — drafts land, the pilot runs, the print
	// vendor comparison is abandoned mid-flight.
	s.pass(22 * time.Hour)
	s.submit("jonas", s.state("protocol", "st-done"))
	s.pass(24 * time.Hour)
	s.submit("jonas", s.state("recruit", "st-done"))
	s.pass(24 * time.Hour)
	s.submit("ana", s.state("msg", "st-done"), s.state("pricing", "st-done"))
	s.pass(24 * time.Hour)
	s.submit("priya", s.state("sessions", "st-done"))
	s.pass(24 * time.Hour)
	s.submit("ana", s.state("faq", "st-done"), s.state("email", "st-done"))
	s.pass(24 * time.Hour)
	s.submit("maria", s.state("brochure", "st-done"))
	s.pass(24 * time.Hour)
	s.submit("ana", s.state("vendor", "st-cancelled"))

	// Day 14: Maria starts the field guide… (active = transition +
	// allocations in ONE batch, the §5.2 invariant)
	s.pass(5 * 24 * time.Hour)
	s.submit("maria",
		s.state("guide", "st-in_progress"),
		s.alloc("al-guide", "guide", "maria", "req-guide", 1),
	)
	// Day 15: …and pauses it (leaving active closes the allocations in the
	// same batch). Held + Maria free again ⇒ resumable-now on the board.
	s.pass(24 * time.Hour)
	s.submit("maria",
		s.state("guide", "st-on_hold"),
		s.closeAlloc("al-guide"),
	)
	// The workshop room goes down for repair (capacity 0 while unavailable).
	s.pass(2 * time.Hour)
	s.submit("daniel", writer.Command{
		Type: event.TypeResourceAvailabilityChanged, V: 1, Entity: s.id("room"),
		Payload: event.ResourceAvailabilityChanged{Available: false, Note: "AV rig failed — vendor repair booked"},
	})

	// Days 16–18: three things go (and stay) active. With Priya allocated
	// and the room down, facilitation capacity is zero: the workshop starts
	// its resource_blocked stint here (§3.3 starvation).
	s.pass(22 * time.Hour)
	s.submit("priya",
		s.state("onboard", "st-in_progress"),
		s.alloc("al-onboard", "onboard", "priya-r", "req-onboard", 1),
	)
	s.pass(24 * time.Hour)
	s.submit("jonas",
		s.state("analyze", "st-in_progress"),
		s.alloc("al-analyze", "analyze", "jonas-r", "req-analyze", 1),
	)
	s.pass(24 * time.Hour)
	s.submit("ana",
		s.state("legal", "st-in_progress"),
		s.alloc("al-legal", "legal", "reviewers", "req-legal", 1),
	)

	// Day 19: the analysis turns out to need a second pair of hands — the
	// requirement is superseded while the thing is active (§2.5), leaving
	// its open allocation out of step until a re-propose reconciles.
	s.pass(24 * time.Hour)
	s.submit("jonas", writer.Command{
		Type: event.TypeRequirementSuperseded, V: 1, Entity: s.id("req-analyze"),
		Payload: event.RequirementSuperseded{Quantity: 2, Capabilities: []string{s.id("cap-data")}},
	})

	return s.err
}
