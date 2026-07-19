package domain_test

import (
	"testing"

	"churn/internal/domain"
	"churn/internal/domain/domaintest"
)

func BenchmarkDepView(b *testing.B) {
	p, err := domain.Fold(domaintest.BigLog(500, 300))
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.DepView()
	}
}
