package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSettingsRoundtrip: defaults when absent, PUT persists, GET returns the
// saved weights, and recommendations pick them up.
func TestSettingsRoundtrip(t *testing.T) {
	e := newEnv(t)

	// Defaults when no file exists.
	m := e.call("GET", "/api/v1/settings", nil, 200)
	if m["immediate_unlock"].(float64) != 2 || m["waiting_age"].(float64) != 3 {
		t.Fatalf("defaults: %v", m)
	}
	if _, err := os.Stat(filepath.Join(e.dir, settingsFileName)); !os.IsNotExist(err) {
		t.Fatalf("settings file exists before first PUT")
	}

	// Full replacement PUT.
	put := map[string]any{
		"immediate_unlock": 5.0, "downstream_reach": 0.5, "remaining_depth": 1.0,
		"waiting_age": 0.0, "scarcity_penalty": 4.0,
	}
	e.call("PUT", "/api/v1/settings", put, 200)
	m = e.call("GET", "/api/v1/settings", nil, 200)
	if m["immediate_unlock"].(float64) != 5 || m["scarcity_penalty"].(float64) != 4 {
		t.Fatalf("after PUT: %v", m)
	}
	if _, err := os.Stat(filepath.Join(e.dir, settingsFileName)); err != nil {
		t.Fatalf("settings file not written: %v", err)
	}

	// Recommendations disclose the live weights.
	rec := e.call("GET", "/api/v1/analytics/recommendations", nil, 200)
	if rec["weights"].(map[string]any)["immediate_unlock"].(float64) != 5 {
		t.Fatalf("recommendations weights: %v", rec["weights"])
	}

	// Partial PUT is rejected (full replacement, like every supersession).
	m = e.call("PUT", "/api/v1/settings", map[string]any{"immediate_unlock": 1.0}, 400)
	if errKind(m) != "bad_request" {
		t.Fatalf("partial PUT: %v", m)
	}
	// Unknown fields are rejected.
	bad := map[string]any{}
	for k, v := range put {
		bad[k] = v
	}
	bad["bogus"] = 1
	e.call("PUT", "/api/v1/settings", bad, 400)
	// Negative weights are rejected (the formula's subtraction is built in).
	neg := map[string]any{}
	for k, v := range put {
		neg[k] = v
	}
	neg["waiting_age"] = -1.0
	m = e.call("PUT", "/api/v1/settings", neg, 400)
	if errKind(m) != "bad_request" {
		t.Fatalf("negative weight: %v", m)
	}
	// The stored file is unchanged by the failed PUTs.
	m = e.call("GET", "/api/v1/settings", nil, 200)
	if m["immediate_unlock"].(float64) != 5 {
		t.Fatalf("failed PUT changed settings: %v", m)
	}
}

// TestSettingsAtomicWrite: a simulated failure mid-save (the temp path is
// unwritable because a directory occupies it) leaves the previous file
// intact and no partial state behind.
func TestSettingsAtomicWrite(t *testing.T) {
	e := newEnv(t)
	good := map[string]any{
		"immediate_unlock": 7.0, "downstream_reach": 1.0, "remaining_depth": 1.0,
		"waiting_age": 1.0, "scarcity_penalty": 1.0,
	}
	e.call("PUT", "/api/v1/settings", good, 200)

	// Occupy the temp path with a directory: os.WriteFile must fail before
	// any byte reaches settings.json.
	tmp := filepath.Join(e.dir, settingsFileName+".tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := map[string]any{
		"immediate_unlock": 9.0, "downstream_reach": 9.0, "remaining_depth": 9.0,
		"waiting_age": 9.0, "scarcity_penalty": 9.0,
	}
	m := e.call("PUT", "/api/v1/settings", bad, 500)
	if errKind(m) != "internal" {
		t.Fatalf("failed save: %v", m)
	}
	// The previous settings survive untouched.
	m = e.call("GET", "/api/v1/settings", nil, 200)
	if m["immediate_unlock"].(float64) != 7 {
		t.Fatalf("settings corrupted by failed save: %v", m)
	}
	// save's own cleanup may already have removed the (empty) blocking dir.
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// And saving works again afterwards.
	e.call("PUT", "/api/v1/settings", bad, 200)
	if got := e.call("GET", "/api/v1/settings", nil, 200); got["immediate_unlock"].(float64) != 9 {
		t.Fatalf("save after recovery: %v", got)
	}
}
