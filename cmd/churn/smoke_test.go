package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSmokeFullBinary builds the real churn binary and drives it end to end:
// serve on a temp workspace + free port, GET / (the embedded UI shell), GET
// the hashed bundle, and one API round-trip. This is the proof that the
// embed.FS wiring survives `go build` — not just httptest.
func TestSmokeFullBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping binary smoke test")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "churn.exe")
	if runtime.GOOS != "windows" {
		bin = filepath.Join(dir, "churn")
	}

	build := exec.Command("go", "build", "-o", bin, "churn/cmd/churn")
	build.Dir = moduleRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	data := filepath.Join(dir, "ws")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	srv := exec.Command(bin, "serve", "--data", data, "--listen", "127.0.0.1:0", "--actor", "smoke", "--no-open")
	stdout, err := srv.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	srv.Stderr = io.Discard
	if err := srv.Start(); err != nil {
		t.Fatalf("starting %s: %v", bin, err)
	}
	defer func() {
		_ = srv.Process.Kill()
		_, _ = srv.Process.Wait()
	}()

	// Parse "listening on http://127.0.0.1:PORT" from stdout.
	addrRe := regexp.MustCompile(`listening on (http://[0-9.:]+)`)
	base := ""
	sc := bufio.NewScanner(stdout)
	deadline := time.After(30 * time.Second)
	lineCh := make(chan string)
	go func() {
		for sc.Scan() {
			lineCh <- sc.Text()
		}
		close(lineCh)
	}()
scan:
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatal("server exited before announcing its address")
			}
			if m := addrRe.FindStringSubmatch(line); m != nil {
				base = m[1]
				break scan
			}
		case <-deadline:
			t.Fatal("timed out waiting for the serve banner")
		}
	}

	get := func(path string) (*http.Response, []byte) {
		t.Helper()
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("GET %s: reading body: %v", path, err)
		}
		return resp, b
	}

	// 1. The UI shell.
	resp, body := get("/")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("GET /: status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	// 2. The hashed bundle referenced by the shell.
	bundleRe := regexp.MustCompile(`/assets/app-[A-Z0-9]+\.js`)
	m := bundleRe.Find(body)
	if m == nil {
		t.Fatalf("GET /: shell references no bundle:\n%s", body)
	}
	resp, jsBody := get(string(m))
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("GET %s: status %d, type %q", m, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if len(jsBody) == 0 {
		t.Fatalf("GET %s: empty bundle", m)
	}

	// 3. One API round-trip: create a project, list shows it.
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/projects",
		bytes.NewReader([]byte(`{"name":"smoke project"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	postBody, _ := io.ReadAll(postResp.Body)
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/projects: status %d: %s", postResp.StatusCode, postBody)
	}
	_, listBody := get("/api/v1/projects")
	var projects []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(listBody, &projects); err != nil {
		t.Fatalf("GET /api/v1/projects: %v: %s", err, listBody)
	}
	found := false
	for _, p := range projects {
		found = found || p.Name == "smoke project"
	}
	if !found {
		t.Fatalf("created project missing from list: %s", listBody)
	}
}

// moduleRoot locates the repo root (where go.mod lives) from the test's cwd.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above " + dir)
		}
		dir = parent
	}
}
