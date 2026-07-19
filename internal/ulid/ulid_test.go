package ulid

import (
	"bytes"
	"math/rand"
	"sort"
	"testing"
	"time"
)

// testEntropy returns a deterministic entropy reader.
func testEntropy(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}

// fixedClock returns a clock stuck at ms milliseconds since the epoch.
func fixedClock(ms int64) func() time.Time {
	return func() time.Time { return time.UnixMilli(ms) }
}

func TestStringRoundTrip(t *testing.T) {
	g := NewGenerator(fixedClock(1721390000000), testEntropy(1))
	for i := 0; i < 1000; i++ {
		u, err := g.New()
		if err != nil {
			t.Fatal(err)
		}
		s := u.String()
		if len(s) != 26 {
			t.Fatalf("length %d: %s", len(s), s)
		}
		back, err := Parse(s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		if back != u {
			t.Fatalf("round-trip mismatch: %v → %s → %v", u, s, back)
		}
	}
}

func TestKnownEncoding(t *testing.T) {
	// All-zero ULID and a known timestamp extraction.
	var zero ULID
	if got := zero.String(); got != "00000000000000000000000000" {
		t.Fatalf("zero ULID: %s", got)
	}
	u, err := Make(1721390000000, [10]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if u.Time() != 1721390000000 {
		t.Fatalf("Time() = %d", u.Time())
	}
	if _, err := Make(1<<48, [10]byte{}); err == nil {
		t.Fatal("48-bit timestamp overflow must be rejected")
	}
}

func TestLexicographicOrderFollowsTime(t *testing.T) {
	ms := int64(1721390000000)
	now := func() time.Time { return time.UnixMilli(ms) }
	g := NewGenerator(now, testEntropy(2))
	var prev string
	for i := 0; i < 500; i++ {
		u, err := g.New()
		if err != nil {
			t.Fatal(err)
		}
		s := u.String()
		if prev != "" && prev >= s {
			t.Fatalf("not increasing at %d: %s then %s", i, prev, s)
		}
		prev = s
		ms += int64(i % 3) // advance 0–2ms: mixes same-ms and cross-ms steps
	}
}

func TestMonotonicWithinSameMillisecond(t *testing.T) {
	g := NewGenerator(fixedClock(1721390000000), testEntropy(3))
	a, err := g.New()
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.New()
	if err != nil {
		t.Fatal(err)
	}
	if a.Time() != b.Time() {
		t.Fatalf("timestamps differ: %d vs %d", a.Time(), b.Time())
	}
	if bytes.Compare(a[:], b[:]) >= 0 {
		t.Fatalf("not strictly increasing: %s then %s", a, b)
	}
	// b must be exactly a+1 in the entropy.
	want, err := incremented(a)
	if err != nil {
		t.Fatal(err)
	}
	if b != want {
		t.Fatalf("expected increment of previous: %v vs %v", b, want)
	}
}

func TestMonotonicAcrossBackwardsClock(t *testing.T) {
	times := []int64{1721390000005, 1721390000010, 1721390000003, 1721390000003, 1721390000010}
	i := 0
	now := func() time.Time { t := time.UnixMilli(times[i]); i++; return t }
	g := NewGenerator(now, testEntropy(4))
	var prev ULID
	for n := 0; n < len(times); n++ {
		u, err := g.New()
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 && bytes.Compare(prev[:], u[:]) >= 0 {
			t.Fatalf("not strictly increasing across backwards clock: %s then %s", prev, u)
		}
		prev = u
	}
}

func TestUniquenessUnderBurst(t *testing.T) {
	g := NewGenerator(fixedClock(1721390000000), testEntropy(5))
	const n = 10000
	seen := make(map[ULID]bool, n)
	strs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		u, err := g.New()
		if err != nil {
			t.Fatal(err)
		}
		if seen[u] {
			t.Fatalf("duplicate ULID %s at %d", u, i)
		}
		seen[u] = true
		strs = append(strs, u.String())
	}
	if !sort.StringsAreSorted(strs) {
		t.Fatal("burst ULIDs not lexicographically sorted")
	}
}

func TestEntropyOverflowCarriesIntoTimestamp(t *testing.T) {
	u, err := Make(1721390000000, [10]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
	if err != nil {
		t.Fatal(err)
	}
	next, err := incremented(u)
	if err != nil {
		t.Fatal(err)
	}
	if next.Time() != u.Time()+1 {
		t.Fatalf("expected carry into timestamp: %d → %d", u.Time(), next.Time())
	}
	if next[6] != 0 || next[15] != 0 {
		t.Fatal("entropy should have wrapped to zero")
	}
}

func TestParseErrors(t *testing.T) {
	cases := []string{
		"",
		"tooshort",
		"0123456789012345678901234",   // 25
		"012345678901234567890123456", // 27
		"01234567890123456789012345",  // ok length, but check below uses invalid chars
	}
	if _, err := Parse(cases[0]); err == nil {
		t.Fatal("empty string must fail")
	}
	for _, s := range cases[1:4] {
		if _, err := Parse(s); err == nil {
			t.Fatalf("%q: expected length error", s)
		}
	}
	// Invalid characters: U, I, L, O are not in the alphabet.
	if _, err := Parse("0000000000000000000000000U"); err == nil {
		t.Fatal("invalid character must fail")
	}
	// First char > 7 overflows 128 bits.
	if _, err := Parse("80000000000000000000000000"); err == nil {
		t.Fatal("first-character overflow must fail")
	}
	// Lowercase is accepted.
	u, err := Parse("01hzabcdefghjkmnpqrstvwxyz")
	if err != nil {
		t.Fatal(err)
	}
	up, err := Parse("01HZABCDEFGHJKMNPQRSTVWXYZ")
	if err != nil {
		t.Fatal(err)
	}
	if u != up {
		t.Fatal("case-insensitive parse mismatch")
	}
}
