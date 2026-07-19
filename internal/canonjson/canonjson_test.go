package canonjson

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestSortedKeysMinimalWhitespace(t *testing.T) {
	got, err := Canonicalize([]byte(` { "b" : 1 , "a" : { "z" : [ 1 , 2 ] , "y" : null } } `))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":{"y":null,"z":[1,2]},"b":1}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestEncodeStruct(t *testing.T) {
	type payload struct {
		WorkspaceID string `json:"workspace_id"`
		Count       int    `json:"count"`
	}
	got, err := Encode(payload{WorkspaceID: "ws_1", Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"count":3,"workspace_id":"ws_1"}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestStringEscaping(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", `"plain"`},
		{`quote " and \ slash`, `"quote \" and \\ slash"`},
		{"\b\f\n\r\t", `"\b\f\n\r\t"`},
		{"<html> & so on", `"<html> & so on"`}, // no HTML escaping
		{"héllo ☃", "\"héllo ☃\""},             // multi-byte passes through
		{"  ", "\"  \""},                       // not escaped: JSON, not JS
	}
	for _, c := range cases {
		got, err := Encode(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if string(got) != c.want {
			t.Errorf("%q: got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestControlCharEscaping(t *testing.T) {
	// Control characters without short forms escape as backslash-u00XX
	// (lowercase hex). Built programmatically to keep raw bytes out of
	// the source.
	in := string([]byte{0x00, 0x1f})
	bs := string(rune(0x5c))
	want := `"` + bs + "u0000" + bs + "u001f" + `"`
	got, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	assertStable(t, got)
}

func TestInvalidUTF8Stabilizes(t *testing.T) {
	// encoding/json replaces invalid UTF-8 with U+FFFD during decode; after
	// one canonicalization the bytes are valid and stable.
	got, err := Encode("bad \xff byte")
	if err != nil {
		t.Fatal(err)
	}
	assertStable(t, got)
}

func TestNumbers(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0", "0"},
		{"-0", "0"},   // int64 path normalizes -0
		{"-0.0", "0"}, // negative zero normalized on the float path too
		{"1", "1"},
		{"1.0", "1"},
		{"1.5", "1.5"},
		{"-2.25", "-2.25"},
		{"1e6", "1e+06"},
		{"1E6", "1e+06"},
		{"9223372036854775807", "9223372036854775807"},   // MaxInt64 exact
		{"-9223372036854775808", "-9223372036854775808"}, // MinInt64 exact
		{"1e21", "1e+21"},
		{"0.000001", "1e-06"},
		{"123456", "123456"},
		{"3.141592653589793", "3.141592653589793"},
	}
	for _, c := range cases {
		got, err := Canonicalize([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if string(got) != c.want {
			t.Errorf("%s: got %s, want %s", c.in, got, c.want)
			continue
		}
		assertStable(t, got)
	}
}

func TestFloatCaveats(t *testing.T) {
	// uint64 above MaxInt64 falls back to float64 and loses precision — the
	// documented caveat, pinned here so a change is deliberate.
	got, err := Encode(uint64(math.MaxUint64))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "1.8446744073709552e+19" {
		t.Fatalf("caveat drifted: got %s", got)
	}
	assertStable(t, got)

	if _, err := Encode(math.NaN()); err == nil {
		t.Fatal("NaN must be rejected")
	}
	if _, err := Encode(math.Inf(1)); err == nil {
		t.Fatal("Inf must be rejected")
	}
}

func TestTrailingDataRejected(t *testing.T) {
	if _, err := Canonicalize([]byte(`{} {}`)); err == nil {
		t.Fatal("trailing data must be rejected")
	}
	if _, err := Canonicalize([]byte(`1 2`)); err == nil {
		t.Fatal("trailing data must be rejected")
	}
}

func TestInvalidJSONRejected(t *testing.T) {
	for _, in := range []string{"", "{", `{"a":}`, "nul", `"unterminated`} {
		if _, err := Canonicalize([]byte(in)); err == nil {
			t.Fatalf("%q: expected error", in)
		}
	}
}

// assertStable checks the core property on already-canonical bytes:
// decode → re-encode is the identity.
func assertStable(t *testing.T, canon []byte) {
	t.Helper()
	again, err := Canonicalize(canon)
	if err != nil {
		t.Fatalf("re-canonicalize %s: %v", canon, err)
	}
	if !bytes.Equal(canon, again) {
		t.Fatalf("not byte-stable: %s → %s", canon, again)
	}
}

func TestRoundTripStabilityProperty(t *testing.T) {
	inputs := []any{
		nil,
		true,
		"x",
		3.75,
		int64(math.MaxInt64),
		map[string]any{"z": []any{1, 2.5, "three", nil}, "a": map[string]any{"nested": true}},
		[]any{map[string]any{"k": "v"}, []any{}, map[string]any{}},
		strings.Repeat("nest \"deep\" \n", 10),
	}
	for _, v := range inputs {
		canon, err := Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		assertStable(t, canon)
		// And the decoded value re-encodes identically via Encode too. The
		// round-trip contract requires number-preserving decoding
		// (UseNumber): decoding into float64 would corrupt int64 values
		// beyond 2^53 before canonjson ever sees them.
		dec := json.NewDecoder(bytes.NewReader(canon))
		dec.UseNumber()
		var decoded any
		if err := dec.Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		re, err := Encode(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(canon, re) {
			t.Fatalf("Encode(decode) not stable: %s → %s", canon, re)
		}
	}
}

func FuzzCanonicalize(f *testing.F) {
	seeds := []string{
		`{}`, `[]`, `null`, `true`, `""`,
		`{"b":1,"a":2}`,
		`{"a":{"y":null,"z":[1,2]},"b":"x"}`,
		`[1,1.0,1e6,-0.0,9223372036854775807,1e21,0.000001]`,
		`"esc \" \ 
 <&> hello"`,
		`{"dup":1,"dup":2}`,
		`[[[[[["deep"]]]]]]`,
		`{"":""}`,
		`-9223372036854775808`,
		`1.7976931348623157e+308`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		canon, err := Canonicalize(data)
		if err != nil {
			return // invalid input is fine; we only claim stability of valid JSON
		}
		again, err := Canonicalize(canon)
		if err != nil {
			t.Fatalf("canonical output failed to re-parse: %s: %v", canon, err)
		}
		if !bytes.Equal(canon, again) {
			t.Fatalf("not byte-stable: %q → %s → %s", data, canon, again)
		}
	})
}
