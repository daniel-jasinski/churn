// Package ulid implements ULIDs (Universally Unique Lexicographically
// Sortable Identifiers): 128-bit values composed of a 48-bit millisecond
// Unix timestamp and 80 bits of entropy, rendered as 26 characters of
// Crockford base32. The string form sorts lexicographically in time order.
//
// The Generator is the only way churn mints ids: its clock and entropy
// source are injected, so tests are deterministic, and it guarantees
// strictly increasing ULIDs within one process even when the wall clock
// stands still or steps backwards.
package ulid

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// ULID is a 16-byte identifier: bytes 0–5 hold the big-endian millisecond
// timestamp, bytes 6–15 the entropy.
type ULID [16]byte

// alphabet is Crockford base32.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// decodeMap maps an ASCII byte to its 5-bit value, or 0xFF if invalid.
// Lowercase is accepted as a Crockford case alias; the ambiguous aliases
// (I, L, O, U) are not. Parse does see foreign bytes — churn import-log
// parses arbitrary JSONL — so callers that need exactly-one-spelling
// semantics (import does: case-aliased ids must not evade duplicate checks)
// must additionally require the canonical form, Parse(s).String() == s.
var decodeMap = func() (m [256]byte) {
	for i := range m {
		m[i] = 0xFF
	}
	for i := 0; i < len(alphabet); i++ {
		m[alphabet[i]] = byte(i)
		m[alphabet[i]|0x20] = byte(i) // lowercase alias (digits unaffected: no letter maps there)
	}
	return
}()

// Make assembles a ULID from a millisecond timestamp and 80 bits of entropy.
// ms must fit in 48 bits.
func Make(ms uint64, entropy [10]byte) (ULID, error) {
	if ms >= 1<<48 {
		return ULID{}, fmt.Errorf("ulid: timestamp %d overflows 48 bits", ms)
	}
	var u ULID
	u[0] = byte(ms >> 40)
	u[1] = byte(ms >> 32)
	u[2] = byte(ms >> 24)
	u[3] = byte(ms >> 16)
	u[4] = byte(ms >> 8)
	u[5] = byte(ms)
	copy(u[6:], entropy[:])
	return u, nil
}

// Time returns the ULID's timestamp as milliseconds since the Unix epoch.
func (u ULID) Time() uint64 {
	return uint64(u[0])<<40 | uint64(u[1])<<32 | uint64(u[2])<<24 |
		uint64(u[3])<<16 | uint64(u[4])<<8 | uint64(u[5])
}

// String renders the ULID as 26 characters of Crockford base32. The 128 bits
// are left-padded with two zero bits to fill 26×5 = 130 bits, so the first
// character is always in [0-7].
func (u ULID) String() string {
	var dst [26]byte
	for i := 0; i < 26; i++ {
		var v byte
		for b := 0; b < 5; b++ {
			pos := i*5 - 2 + b // bit index into the 128-bit value
			v <<= 1
			if pos >= 0 {
				v |= (u[pos/8] >> (7 - pos%8)) & 1
			}
		}
		dst[i] = alphabet[v]
	}
	return string(dst[:])
}

// Parse decodes the 26-character string form produced by String.
func Parse(s string) (ULID, error) {
	if len(s) != 26 {
		return ULID{}, fmt.Errorf("ulid: %q: length %d, want 26", s, len(s))
	}
	var u ULID
	for i := 0; i < 26; i++ {
		v := decodeMap[s[i]]
		if v == 0xFF {
			return ULID{}, fmt.Errorf("ulid: %q: invalid character %q at %d", s, s[i], i)
		}
		if i == 0 && v > 7 {
			return ULID{}, fmt.Errorf("ulid: %q: first character overflows 128 bits", s)
		}
		for b := 0; b < 5; b++ {
			pos := i*5 - 2 + b
			if pos < 0 {
				continue
			}
			if v&(1<<(4-b)) != 0 {
				u[pos/8] |= 1 << (7 - pos%8)
			}
		}
	}
	return u, nil
}

// Generator mints strictly increasing ULIDs. Both the clock and the entropy
// source are injected; a deterministic reader plus a fixed clock yields a
// fully reproducible id sequence.
//
// Monotonicity: within one millisecond (or when the clock stands still or
// steps backwards) each new ULID is the previous one plus one in the 80-bit
// entropy; on the astronomically unlikely entropy overflow the generator
// advances into the next millisecond rather than fail. Consequently ids are
// strictly increasing — lexicographically and bytewise — for the life of
// the Generator.
type Generator struct {
	mu      sync.Mutex
	now     func() time.Time
	entropy io.Reader
	last    ULID
	primed  bool
}

// NewGenerator returns a Generator drawing time from now and randomness from
// entropy (e.g. crypto/rand.Reader in production, a seeded math/rand reader
// in tests).
func NewGenerator(now func() time.Time, entropy io.Reader) *Generator {
	return &Generator{now: now, entropy: entropy}
}

// New mints the next ULID. Safe for concurrent use.
func (g *Generator) New() (ULID, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ms := uint64(g.now().UnixMilli())
	if g.primed && ms <= g.last.Time() {
		// Same millisecond, stalled or backwards clock: increment the
		// previous ULID. incremented handles entropy overflow by carrying
		// into the timestamp.
		next, err := incremented(g.last)
		if err != nil {
			return ULID{}, err
		}
		g.last = next
		return next, nil
	}
	var entropy [10]byte
	if _, err := io.ReadFull(g.entropy, entropy[:]); err != nil {
		return ULID{}, fmt.Errorf("ulid: reading entropy: %w", err)
	}
	u, err := Make(ms, entropy)
	if err != nil {
		return ULID{}, err
	}
	g.last = u
	g.primed = true
	return u, nil
}

// incremented returns u plus one as a 128-bit big-endian integer. An
// entropy overflow naturally carries into the timestamp — the intended
// "advance into the next millisecond" behavior. Errors only on overflow of
// the full 128 bits.
func incremented(u ULID) (ULID, error) {
	for i := 15; i >= 0; i-- {
		u[i]++
		if u[i] != 0 {
			return u, nil
		}
	}
	return ULID{}, fmt.Errorf("ulid: 128-bit overflow")
}
