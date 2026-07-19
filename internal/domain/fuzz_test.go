package domain_test

import (
	"fmt"
	"reflect"
	"testing"

	"churn/internal/domain"
	"churn/internal/domain/domaintest"
	"churn/internal/event"
)

func fuzzInit() event.Envelope {
	return event.Envelope{
		Seq: 1, ID: "ev_init", Origin: "wr_fuzz", Batch: "b_init",
		TS: "2026-07-19T10:00:00.000Z", Actor: "system",
		Type: event.TypeLogInitialized, V: 1,
		Data: []byte(`{"workspace_id":"ws_fuzz"}`),
	}
}

// TestInvariantFuzz drives a few hundred random valid batches per seed
// through ValidateBatch and asserts every global invariant after each one:
// expanded graph acyclic, containment a tree, composites stateless and
// requirement-free, open allocations on active leaves with live references,
// capacity/version accounting consistent, no dangling references anywhere.
func TestInvariantFuzz(t *testing.T) {
	const batches = 400
	for _, seed := range []int64{1, 7, 42} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			init := fuzzInit()
			p, err := domain.Fold([]event.Envelope{init})
			if err != nil {
				t.Fatal(err)
			}
			g := domaintest.NewGenerator(seed)
			all := []event.Envelope{init}
			applied := 0
			for i := 0; i < batches; i++ {
				cmds := g.Next(p)
				if cmds == nil {
					continue
				}
				evs := domaintest.Envelopes(p, fmt.Sprintf("b_%06d", i+1), cmds)
				cand, err := domain.ValidateBatch(p, evs, nil)
				if err != nil {
					t.Fatalf("batch %d unexpectedly rejected: %v", i, err)
				}
				p = cand
				all = append(all, evs...)
				applied++
				if err := domaintest.CheckInvariants(p); err != nil {
					t.Fatalf("invariant violated after batch %d (seed %d): %v", i, seed, err)
				}
			}
			if applied < batches/2 {
				t.Fatalf("only %d/%d batches applied — generator starved", applied, batches)
			}

			// Replay determinism at the fold level: refolding the full event
			// stream reproduces the incrementally validated projection —
			// INCLUDING the §3.3 status-entry bookkeeping (Statuses: derived
			// status, entry timestamps, cumulative resource-blocked credits),
			// which the generator's synthetic per-batch timestamps genuinely
			// exercise (all three seeds produce resource_blocked stints and
			// nonzero retained credits).
			replayed, err := domain.Fold(all)
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			if !reflect.DeepEqual(p, replayed) {
				t.Fatalf("replay diverged from live projection after %d batches", applied)
			}
			t.Logf("seed %d: %d batches, %d events, %d cycle proposals skipped, %d things, %d deps, %d allocations",
				seed, applied, len(all), g.Skipped, len(p.Things), len(p.Dependencies), len(p.Allocations))
		})
	}
}
