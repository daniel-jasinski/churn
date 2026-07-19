// Package canonjson encodes values as canonical JSON: object keys sorted
// bytewise, minimal whitespace, and a stable number formatting, so that a
// canonical document decoded and re-encoded reproduces itself byte for byte.
//
// Canonical form:
//
//   - Objects: keys sorted by byte order, no duplicate handling beyond
//     encoding/json's last-wins rule, no whitespace.
//   - Strings: minimal escaping — only `"`, `\`, and control characters are
//     escaped; \b \f \n \r \t use their short forms, other controls use
//     \u00XX. HTML characters (<, >, &) are not escaped.
//   - Numbers: integers that fit an int64 are written in plain base-10 form;
//     everything else is parsed as a float64 and written with
//     strconv.FormatFloat(f, 'g', -1, 64), the shortest representation that
//     round-trips. Both forms are idempotent under decode→re-encode.
//
// Number caveats (payloads are produced from Go structs, so these are the
// rules of the road, not surprises):
//
//   - Non-integer numbers pass through float64. A value such as 1.0 encodes
//     as 1; precision beyond float64 is lost.
//   - uint64 values above math.MaxInt64 do not fit the int64 fast path and
//     fall back to float64, losing precision. Do not put such values in
//     payloads.
//   - NaN and infinities are rejected (by encoding/json, as usual).
//   - Negative zero is normalized to 0.
//
// The byte-stability guarantee (encode → decode → re-encode is the identity)
// holds under number-preserving decoding (json.Decoder with UseNumber, as
// Canonicalize itself does). Decoding into float64 corrupts int64 values
// beyond 2^53 before this package ever sees them.
package canonjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf8"
)

// Encode marshals v with encoding/json and returns its canonical form.
func Encode(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Canonicalize(raw)
}

// Canonicalize parses raw JSON and re-encodes it canonically.
// It is idempotent: Canonicalize(Canonicalize(x)) == Canonicalize(x).
func Canonicalize(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	// Reject trailing content so a canonical document is exactly one value.
	if dec.More() {
		return nil, fmt.Errorf("canonjson: trailing data after JSON value")
	}
	var buf bytes.Buffer
	if err := write(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func write(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		return writeNumber(buf, string(x))
	case string:
		writeString(buf, x)
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := write(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, k)
			buf.WriteByte(':')
			if err := write(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonjson: unexpected decoded type %T", v)
	}
	return nil
}

func writeNumber(buf *bytes.Buffer, s string) error {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		buf.WriteString(strconv.FormatInt(i, 10))
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("canonjson: unrepresentable number %q: %w", s, err)
	}
	if f == 0 {
		// Normalize negative zero: "-0" would re-decode down the int64 path
		// as plain "0" and break byte stability.
		buf.WriteByte('0')
		return nil
	}
	// Shortest round-trip form. Idempotent with the int64 fast path above:
	// when 'g' emits a plain integer (e.g. 1.0 → "1") the value is far below
	// 2^63 — shortest float64 digits never exceed 17, so anything ≥ 2^63
	// keeps its exponent — and the re-decoded literal takes the int64 path
	// to the identical bytes.
	buf.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	return nil
}

// writeString escapes s minimally, per the package rules.
func writeString(buf *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	buf.WriteByte('"')
	for i := 0; i < len(s); {
		b := s[i]
		if b >= 0x20 && b != '"' && b != '\\' {
			// Multi-byte runes pass through verbatim; encoding/json has
			// already replaced invalid UTF-8 with U+FFFD during decode.
			_, size := utf8.DecodeRuneInString(s[i:])
			buf.WriteString(s[i : i+size])
			i += size
			continue
		}
		switch b {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			buf.WriteString(`\u00`)
			buf.WriteByte(hex[b>>4])
			buf.WriteByte(hex[b&0xf])
		}
		i++
	}
	buf.WriteByte('"')
}
