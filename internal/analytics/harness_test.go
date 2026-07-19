package analytics_test

import (
	"fmt"
	"testing"

	"churn/internal/domain"
	"churn/internal/event"
)

// c3 is the compact test form of one event: type, entity, payload JSON.
type c3 struct {
	typ    string
	entity string
	data   string
}

// ws drives ValidateBatch against an evolving projection with controllable
// batch commit timestamps.
type ws struct {
	t  *testing.T
	p  *domain.Projection
	n  int
	ts string
}

const ts0 = "2026-07-19T10:00:00.000Z"

// newWS builds a workspace with baseline vocabulary: the five semantics as
// states (plus a second active state), two thing types, two capabilities,
// and two projects.
func newWS(t *testing.T) *ws {
	init := event.Envelope{
		Seq: 1, ID: "ev_init", Origin: "wr_t", Batch: "b_init",
		TS: ts0, Actor: "test",
		Type: event.TypeLogInitialized, V: 1,
		Data: []byte(`{"workspace_id":"ws_t"}`),
	}
	p, err := domain.Fold([]event.Envelope{init})
	if err != nil {
		t.Fatal(err)
	}
	w := &ws{t: t, p: p, ts: ts0}
	w.batch(
		c3{event.TypeStateDefined, "st_todo", `{"name":"todo","semantic":"pending"}`},
		c3{event.TypeStateDefined, "st_act", `{"name":"in_progress","semantic":"active"}`},
		c3{event.TypeStateDefined, "st_act2", `{"name":"executing","semantic":"active"}`},
		c3{event.TypeStateDefined, "st_done", `{"name":"done","semantic":"satisfied"}`},
		c3{event.TypeStateDefined, "st_hold", `{"name":"on_hold","semantic":"paused"}`},
		c3{event.TypeStateDefined, "st_cancel", `{"name":"cancelled","semantic":"abandoned"}`},
		c3{event.TypeTypeDefined, "ty_task", `{"name":"task"}`},
		c3{event.TypeTypeDefined, "ty_review", `{"name":"review"}`},
		c3{event.TypeCapabilityDefined, "cap_edit", `{"name":"editing"}`},
		c3{event.TypeCapabilityDefined, "cap_appr", `{"name":"approval"}`},
		c3{event.TypeProjectCreated, "pr_1", `{"name":"Alpha"}`},
		c3{event.TypeProjectCreated, "pr_2", `{"name":"Beta"}`},
	)
	return w
}

// at sets the commit timestamp for subsequent batches (must be monotone).
func (w *ws) at(ts string) *ws {
	w.ts = ts
	return w
}

// batch validates and applies one batch; rejection is fatal.
func (w *ws) batch(cmds ...c3) {
	w.t.Helper()
	w.n++
	evs := make([]event.Envelope, len(cmds))
	for i, c := range cmds {
		seq := w.p.LastSeq + 1 + int64(i)
		evs[i] = event.Envelope{
			Seq:    seq,
			ID:     fmt.Sprintf("ev_%06d", seq),
			Origin: "wr_t",
			Batch:  fmt.Sprintf("b_%04d", w.n),
			TS:     w.ts,
			Actor:  "test",
			Type:   c.typ,
			V:      1,
			Entity: c.entity,
			Data:   []byte(c.data),
		}
	}
	p, err := domain.ValidateBatch(w.p, evs, nil)
	if err != nil {
		w.t.Fatalf("batch unexpectedly rejected: %v", err)
	}
	w.p = p
}

func thing(id, name string) c3 {
	return c3{event.TypeThingCreated, id, fmt.Sprintf(`{"name":%q,"project":"pr_1","type":"ty_task"}`, name)}
}

func childThing(id, name, parent string) c3 {
	return c3{event.TypeThingCreated, id, fmt.Sprintf(`{"name":%q,"parent":%q,"project":"pr_1","type":"ty_task"}`, name, parent)}
}

func state(id, st string) c3 {
	return c3{event.TypeThingStateChanged, id, fmt.Sprintf(`{"state":%q}`, st)}
}

func dep(id, from, to string) c3 {
	return c3{event.TypeDependencyAsserted, id, fmt.Sprintf(`{"from":%q,"to":%q}`, from, to)}
}
