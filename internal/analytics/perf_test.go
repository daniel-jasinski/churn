package analytics_test

import (
	"testing"
	"time"

	"churn/internal/analytics"
	"churn/internal/domain"
	"churn/internal/domain/domaintest"
)

// TestAnalyticsPerfSanity pins §3.1's "everyday screen" requirement at
// realistic scale (500 things / 300 dependency edges): Criticalities,
// Recommend, and Ready must each finish well under the generous 1s bound —
// a regression back to seconds fails loudly. Guarded by -short so quick
// developer loops can skip it.
func TestAnalyticsPerfSanity(t *testing.T) {
	if testing.Short() {
		t.Skip("perf sanity skipped in -short")
	}
	p, err := domain.Fold(domaintest.BigLog(500, 300))
	if err != nil {
		t.Fatal(err)
	}
	s := analytics.DefaultSettings()

	const bound = time.Second
	measure := func(name string, f func()) {
		t.Helper()
		start := time.Now()
		f()
		d := time.Since(start)
		t.Logf("%s at %d things / %d deps: %v", name, len(p.Things), len(p.Dependencies), d)
		if d > bound {
			t.Errorf("%s took %v, bound %v", name, d, bound)
		}
	}
	measure("Criticalities", func() { analytics.Criticalities(p) })
	measure("Recommend", func() { analytics.Recommend(p, s) })
	measure("Ready", func() { analytics.Ready(p, s, analytics.ReadyFilter{}) })
	measure("Contention", func() { analytics.Contention(p) })
	measure("BlockedBy(all)", func() {
		for id := range p.Things {
			analytics.BlockedBy(p, id)
		}
	})
}
