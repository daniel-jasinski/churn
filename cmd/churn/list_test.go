package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListCommands(t *testing.T) {
	dir := t.TempDir()
	buildWorkspace(t, dir) // project "Alpha", type "task", 3 things

	// projects: the table names the project.
	out, _, err := runCLI(t, "ls", "projects", "--data", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "Alpha") {
		t.Fatalf("ls projects: %q", out)
	}

	// things: all three appear; the default kind is things.
	out, _, err = runCLI(t, "ls", "things", "--data", dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if !strings.Contains(out, fmt.Sprintf("thing %d", i)) {
			t.Fatalf("ls things missing thing %d: %q", i, out)
		}
	}
	if def, _, _ := runCLI(t, "ls", "--data", dir); def != out {
		t.Fatalf("default kind should equal 'things':\n%q\nvs\n%q", def, out)
	}

	// --json is a valid array of the three things.
	jsonOut, _, err := runCLI(t, "ls", "things", "--data", dir, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &rows); err != nil {
		t.Fatalf("ls --json is not valid JSON: %v: %s", err, jsonOut)
	}
	if len(rows) != 3 {
		t.Fatalf("ls things --json: %d rows, want 3", len(rows))
	}

	// resources: none in this workspace, but the header still prints and JSON
	// is an empty array (never null).
	rjson, _, err := runCLI(t, "ls", "resources", "--data", dir, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(rjson) != "[]" {
		t.Fatalf("empty resources JSON = %q, want []", strings.TrimSpace(rjson))
	}

	// an unknown kind is rejected.
	if _, _, err := runCLI(t, "ls", "widgets", "--data", dir); err == nil ||
		!strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("ls widgets: got %v, want an unknown-kind error", err)
	}
}

// TestListKindFirstParsing: the kind may lead and flags may follow it
// ("ls things --json"), despite Go's flag package stopping at the first
// positional.
func TestListKindFirstParsing(t *testing.T) {
	dir := t.TempDir()
	buildWorkspace(t, dir)
	if _, _, err := runCLI(t, "ls", "projects", "--data", dir, "--json"); err != nil {
		t.Fatalf("kind-first with trailing flags: %v", err)
	}
}

func TestHelpForCommand(t *testing.T) {
	// `help <command>` shows that command's usage.
	_, errOut, _ := runCLI(t, "help", "serve")
	if !strings.Contains(errOut, "usage: churn serve") {
		t.Fatalf("help serve: %q", errOut)
	}
	// bare help prints the top-level usage to stdout.
	out, err := func() (string, error) { o, _, e := runCLI(t, "help"); return o, e }()
	if err != nil || !strings.Contains(out, "commands:") {
		t.Fatalf("help: %v %q", err, out)
	}
}

func TestSuggestCommand(t *testing.T) {
	cases := map[string]string{
		"serv":                 "serve",
		"vrsion":               "version",
		"reindx":               "reindex",
		"lss":                  "ls",
		"wildly-different-xyz": "",
	}
	for in, want := range cases {
		if got := suggestCommand(in); got != want {
			t.Errorf("suggestCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExportPositionalPath(t *testing.T) {
	dir := t.TempDir()
	n := buildWorkspace(t, dir)

	// The output file as a positional argument (consistent with backup).
	f := filepath.Join(t.TempDir(), "log.jsonl")
	if _, _, err := runCLI(t, "export-log", "--data", dir, f); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimRight(string(raw), "\n"), "\n") + 1; lines != n {
		t.Fatalf("positional export has %d lines, want %d", lines, n)
	}

	// Passing both --out and a different positional is a conflict.
	if _, _, err := runCLI(t, "export-log", "--data", dir, "--out", "a.jsonl", "b.jsonl"); err == nil {
		t.Fatal("conflicting --out and positional must error")
	}
}
