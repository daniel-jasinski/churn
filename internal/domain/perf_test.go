package domain_test

import (
	"fmt"
	"testing"

	"churn/internal/domain"
	"churn/internal/domain/domaintest"
	"churn/internal/event"
)

// bigProjection folds the shared perf fixture once per benchmark.
func bigProjection(b *testing.B) *domain.Projection {
	b.Helper()
	p, err := domain.Fold(domaintest.BigLog(500, 300))
	if err != nil {
		b.Fatal(err)
	}
	return p
}

// BenchmarkBatchRefresh measures the writer-path cost of one trivial batch
// at 500-thing scale: clone + validate + the batch-boundary status refresh
// (§3.3) — the per-batch constant that gates both live appends and §5's
// replay-in-milliseconds budget.
func BenchmarkBatchRefresh(b *testing.B) {
	p := bigProjection(b)
	evs := []event.Envelope{{
		Seq: p.LastSeq + 1, ID: "ev_bench", Origin: "wr_big", Batch: "b_bench",
		TS: p.LastTS, Actor: "perf",
		Type: event.TypeThingCreated, V: 1, Entity: "th_bench",
		Data: []byte(fmt.Sprintf(`{"name":"bench","project":"pr_big","type":"ty_t"}`)),
	}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := domain.ValidateBatch(p, evs, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDeriveAll measures one full derived-status sweep at scale — the
// building block of every analytics entry point.
func BenchmarkDeriveAll(b *testing.B) {
	p := bigProjection(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.DeriveAll()
	}
}

// BenchmarkFoldBoundaryHeavy is the §5 replay-budget probe: the perf
// fixture re-batched so that EVERY event is its own batch — the worst-case
// number of status-boundary refreshes a fold can pay (~1000 boundaries at
// growing workspace size).
func BenchmarkFoldBoundaryHeavy(b *testing.B) {
	evs := domaintest.BigLog(500, 300)
	for i := range evs {
		evs[i].Batch = fmt.Sprintf("b_%06d", i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := domain.Fold(evs); err != nil {
			b.Fatal(err)
		}
	}
}
