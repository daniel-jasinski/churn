package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"churn/internal/domain"
	"churn/internal/event"
	"churn/internal/interchange"
	"churn/internal/store"
	"churn/internal/writer"
)

// syncBuf is a Writer safe for cross-goroutine use (the serve test reads the
// buffer while the server goroutine writes it).
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// runCLI invokes the command in-process, capturing stdout and stderr.
func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(context.Background(), args, strings.NewReader(""), &out, &errOut)
	return out.String(), errOut.String(), err
}

// buildWorkspace creates a workspace with a few real batches and returns the
// number of events in its log.
func buildWorkspace(t *testing.T, dir string) int {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := writer.Open(st, writer.Options{})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	prID, err := w.MintID(event.PrefixProject)
	if err != nil {
		t.Fatal(err)
	}
	tyID, err := w.MintID(event.PrefixType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Submit("daniel", []writer.Command{
		{Type: event.TypeProjectCreated, V: 1, Entity: prID, Payload: event.ProjectCreated{Name: "Alpha"}},
		{Type: event.TypeTypeDefined, V: 1, Entity: tyID, Payload: event.TypeDefined{Name: "task"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		thID, err := w.MintID(event.PrefixThing)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Submit("daniel", []writer.Command{
			{Type: event.TypeThingCreated, V: 1, Entity: thID, Payload: event.ThingCreated{
				Project: prID, Name: fmt.Sprintf("thing %d", i), Type: tyID,
			}},
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	n := int(w.Projection().LastSeq)
	w.Close()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestDataDirResolution pins the --data > CHURN_DATA > cwd precedence.
func TestDataDirResolution(t *testing.T) {
	t.Setenv("CHURN_DATA", "")
	if got := resolveDataDir("explicit"); got != "explicit" {
		t.Errorf("flag should win: got %q", got)
	}
	if got := resolveDataDir(""); got != "." {
		t.Errorf("no flag/env should default to cwd: got %q", got)
	}
	t.Setenv("CHURN_DATA", "from-env")
	if got := resolveDataDir(""); got != "from-env" {
		t.Errorf("CHURN_DATA should be used when --data is empty: got %q", got)
	}
	if got := resolveDataDir("flag"); got != "flag" {
		t.Errorf("flag should beat env: got %q", got)
	}
}

// TestChurnDataEnvHonored: with no --data, a maintenance command resolves the
// workspace from CHURN_DATA and still fails closed on a missing one, naming it.
func TestChurnDataEnvHonored(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "via-env")
	t.Setenv("CHURN_DATA", missing)
	_, _, err := runCLI(t, "reindex") // no --data
	if err == nil || !strings.Contains(err.Error(), "no workspace database") {
		t.Fatalf("reindex via CHURN_DATA on a missing workspace: got %v", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error should name the env-provided path %q: %v", missing, err)
	}
}

// TestResolveListenAddr pins the serve address rules: --listen wins outright,
// otherwise --port beats CHURN_PORT beats the default, bound to loopback.
func TestResolveListenAddr(t *testing.T) {
	want := fmt.Sprintf("127.0.0.1:%d", defaultPort)
	t.Setenv("CHURN_PORT", "")
	if a, err := resolveListenAddr("", defaultPort, false); err != nil || a != want {
		t.Fatalf("default: %q %v, want %q", a, err, want)
	}
	if a, _ := resolveListenAddr("0.0.0.0:9000", defaultPort, true); a != "0.0.0.0:9000" {
		t.Fatalf("--listen should win verbatim: %q", a)
	}
	t.Setenv("CHURN_PORT", "7000")
	if a, _ := resolveListenAddr("", 5555, true); a != "127.0.0.1:5555" {
		t.Fatalf("--port should beat env: %q", a)
	}
	if a, _ := resolveListenAddr("", defaultPort, false); a != "127.0.0.1:7000" {
		t.Fatalf("CHURN_PORT should be used when --port is unset: %q", a)
	}
	t.Setenv("CHURN_PORT", "nope")
	if _, err := resolveListenAddr("", defaultPort, false); err == nil {
		t.Fatal("a non-numeric CHURN_PORT must error")
	}
	t.Setenv("CHURN_PORT", "")
	if _, err := resolveListenAddr("", 70000, true); err == nil {
		t.Fatal("an out-of-range port must error")
	}
}

// TestVersionCommand: version / --version / -v all print a build line.
func TestVersionCommand(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		out, _, err := runCLI(t, arg)
		if err != nil {
			t.Fatalf("%s: %v", arg, err)
		}
		if !strings.HasPrefix(out, "churn ") || !strings.Contains(out, runtime.GOOS) {
			t.Fatalf("%s output: %q", arg, out)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	_, errOut, err := runCLI(t, "explode")
	if err == nil || !strings.Contains(err.Error(), `unknown command "explode"`) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(errOut, "usage:") {
		t.Fatal("usage not printed for unknown command")
	}
	if _, _, err := runCLI(t); err == nil {
		t.Fatal("no command must be an error")
	}
}

func TestExportImportReindexBackupHappyPath(t *testing.T) {
	dir := t.TempDir()
	n := buildWorkspace(t, dir)

	// export-log --out
	outFile := filepath.Join(t.TempDir(), "log.jsonl")
	stdout, _, err := runCLI(t, "export-log", "--data", dir, "--out", outFile)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Fatalf("export with --out wrote to stdout: %q", stdout)
	}
	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("export has %d lines, want %d", len(lines), n)
	}
	if !strings.Contains(lines[0], `"type":"log.initialized"`) {
		t.Fatalf("first line is not log.initialized: %s", lines[0])
	}

	// export-log to stdout is identical.
	stdout, _, err = runCLI(t, "export-log", "--data", dir)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != string(raw) {
		t.Fatal("stdout export differs from --out export")
	}

	// import-log into a fresh dir, then re-export byte-identically.
	dir2 := filepath.Join(t.TempDir(), "restored")
	stdout, _, err = runCLI(t, "import-log", "--data", dir2, outFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, fmt.Sprintf("imported %d events", n)) {
		t.Fatalf("import summary %q lacks event count %d", stdout, n)
	}
	stdout, _, err = runCLI(t, "export-log", "--data", dir2)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != string(raw) {
		t.Fatal("round-tripped export is not byte-identical")
	}

	// import-log from stdin ("-").
	dir3 := filepath.Join(t.TempDir(), "restored-stdin")
	var out, errOut bytes.Buffer
	if err := run(context.Background(), []string{"import-log", "--data", dir3, "-"},
		bytes.NewReader(raw), &out, &errOut); err != nil {
		t.Fatalf("import from stdin: %v (stderr %s)", err, errOut.String())
	}

	// reindex
	stdout, _, err = runCLI(t, "reindex", "--data", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "event_refs rebuilt") {
		t.Fatalf("reindex output: %q", stdout)
	}

	// backup
	dest := filepath.Join(t.TempDir(), "snap.db")
	if _, _, err := runCLI(t, "backup", "--data", dir, dest); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dest); err != nil || fi.Size() == 0 {
		t.Fatalf("backup file missing or empty: %v", err)
	}
}

// TestMaintenanceCommandsRequireExistingWorkspace pins the typo'd-path rule:
// only serve (and import-log into an empty dir) may create files; reindex,
// export-log, and backup on a nonexistent workspace must error and create
// NOTHING — never mint an empty workspace out of a mistyped --data.
func TestMaintenanceCommandsRequireExistingWorkspace(t *testing.T) {
	for _, tc := range [][]string{
		{"reindex"},
		{"export-log"},
		{"backup"},
	} {
		missing := filepath.Join(t.TempDir(), "no-such-workspace")
		args := append([]string{tc[0], "--data", missing}, tc[1:]...)
		if tc[0] == "backup" {
			args = append(args, filepath.Join(t.TempDir(), "snap.db"))
		}
		if _, _, err := runCLI(t, args...); err == nil ||
			!strings.Contains(err.Error(), "no workspace database") {
			t.Errorf("%s on missing workspace: got %v, want a no-workspace error", tc[0], err)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Errorf("%s created files at the missing path: %v", tc[0], err)
		}
	}
}

func TestImportLogRefusesNonEmptyDir(t *testing.T) {
	src := t.TempDir()
	buildWorkspace(t, src)
	outFile := filepath.Join(t.TempDir(), "log.jsonl")
	if _, _, err := runCLI(t, "export-log", "--data", src, "--out", outFile); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "import-log", "--data", dir, outFile)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("got %v, want not-empty refusal", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "stray.txt" {
		t.Fatalf("dir contents changed: %v", entries)
	}
}

func TestReindexAndServeRefuseHeldWorkspace(t *testing.T) {
	dir := t.TempDir()
	buildWorkspace(t, dir)
	st, err := store.Open(dir) // hold the lock, as a running server would
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, _, err := runCLI(t, "reindex", "--data", dir); err == nil ||
		!strings.Contains(err.Error(), "in use by another churn process") {
		t.Fatalf("reindex on a held workspace: got %v", err)
	}
	if _, _, err := runCLI(t, "serve", "--data", dir, "--listen", "127.0.0.1:0", "--no-open"); err == nil ||
		!strings.Contains(err.Error(), "in use by another churn process") {
		t.Fatalf("serve on a held workspace: got %v", err)
	}

	// export-log and backup are read-only and must work against the held
	// workspace (the live-server case).
	if _, _, err := runCLI(t, "export-log", "--data", dir); err != nil {
		t.Fatalf("export-log against a held workspace: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "snap.db")
	if _, _, err := runCLI(t, "backup", "--data", dir, dest); err != nil {
		t.Fatalf("backup against a held workspace: %v", err)
	}
}

func TestServeHealthEndpoint(t *testing.T) {
	dir := t.TempDir()
	buildWorkspace(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &syncBuf{}
	errOut := &syncBuf{}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--data", dir, "--listen", "127.0.0.1:0", "--no-open"},
			strings.NewReader(""), out, errOut)
	}()

	// Wait for the listen line, then hit the health endpoint.
	var addr string
	deadline := time.Now().Add(10 * time.Second)
	for addr == "" {
		if time.Now().After(deadline) {
			t.Fatalf("server did not report an address; stdout=%q stderr=%q", out.String(), errOut.String())
		}
		if s := out.String(); strings.Contains(s, "listening on http://") {
			s = s[strings.Index(s, "http://"):]
			addr = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	resp, err := http.Get(addr + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 512)
	nr, _ := resp.Body.Read(body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: %d %s", resp.StatusCode, body[:nr])
	}
	if !strings.Contains(string(body[:nr]), `"status":"ok"`) ||
		!strings.Contains(string(body[:nr]), `"workspace_id":"ws_`) {
		t.Fatalf("health body: %s", body[:nr])
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down on context cancel")
	}
}

// TestBackupUnderConcurrentWrites is the M4 backup gate: while a writer
// appends batches, churn backup snapshots the workspace; the snapshot is a
// valid complete-batch prefix of the source log and passes full validation.
func TestBackupUnderConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	w, err := writer.Open(st, writer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	stop := make(chan struct{})
	writes := make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				writes <- nil
				return
			default:
			}
			id, err := w.MintID(event.PrefixProject)
			if err == nil {
				_, err = w.Submit("load", []writer.Command{
					{Type: event.TypeProjectCreated, V: 1, Entity: id,
						Payload: event.ProjectCreated{Name: fmt.Sprintf("p%d", i)}},
				}, nil)
			}
			if err != nil {
				writes <- err
				return
			}
		}
	}()

	// Let some batches land, then back up mid-stream via the CLI path.
	for w.Projection().LastSeq < 25 {
		time.Sleep(time.Millisecond)
	}
	dest := filepath.Join(t.TempDir(), "snap.db")
	if _, _, err := runCLI(t, "backup", "--data", dir, dest); err != nil {
		t.Fatal(err)
	}
	close(stop)
	if err := <-writes; err != nil {
		t.Fatalf("concurrent writes failed: %v", err)
	}

	// Source log after quiescence.
	var source []event.Envelope
	if err := st.Scan(func(ev event.Envelope) error { source = append(source, ev); return nil }); err != nil {
		t.Fatal(err)
	}

	// Open the snapshot as a workspace and read it back.
	bdir := t.TempDir()
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bdir, store.DBFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	bst, err := store.Open(bdir)
	if err != nil {
		t.Fatal(err)
	}
	var snap []event.Envelope
	if err := bst.Scan(func(ev event.Envelope) error { snap = append(snap, ev); return nil }); err != nil {
		t.Fatal(err)
	}

	// A non-trivial, seq-contiguous, complete-batch prefix of the source.
	if len(snap) < 25 || len(snap) > len(source) {
		t.Fatalf("snapshot has %d events, source %d", len(snap), len(source))
	}
	for i, ev := range snap {
		if ev.Seq != int64(i+1) {
			t.Fatalf("snapshot seq %d at position %d", ev.Seq, i)
		}
		src := source[i]
		if ev.ID != src.ID || ev.Batch != src.Batch || string(ev.Data) != string(src.Data) {
			t.Fatalf("snapshot event %d diverges from source", i)
		}
	}
	lastBatch := snap[len(snap)-1].Batch
	inSource := 0
	for _, ev := range source {
		if ev.Batch == lastBatch {
			inSource++
		}
	}
	inSnap := 0
	for _, ev := range snap {
		if ev.Batch == lastBatch {
			inSnap++
		}
	}
	if inSnap != inSource {
		t.Fatalf("snapshot cut batch %s: %d of %d events", lastBatch, inSnap, inSource)
	}

	// The snapshot folds…
	if _, err := domain.Fold(snap); err != nil {
		t.Fatalf("snapshot does not fold: %v", err)
	}
	// …and passes FULL import validation: export it and restore it.
	var buf bytes.Buffer
	if err := interchange.Export(bst, &buf); err != nil {
		t.Fatal(err)
	}
	if err := bst.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := interchange.Import(filepath.Join(t.TempDir(), "revalidated"), &buf); err != nil {
		t.Fatalf("snapshot failed full validation: %v", err)
	}
}
