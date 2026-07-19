package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSSECommitNotification: a subscriber receives the hello event and then
// one commit notification per committed batch, carrying seq, batch, and the
// event types.
func TestSSECommitNotification(t *testing.T) {
	e := newEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", e.ts.URL+"/api/v1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("SSE content type %q", ct)
	}

	sc := bufio.NewScanner(resp.Body)
	readEvent := func() (string, map[string]any) {
		t.Helper()
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				var m map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m); err != nil {
					t.Fatalf("SSE data: %v", err)
				}
				return name, m
			}
		}
		t.Fatalf("SSE stream ended: %v", sc.Err())
		return "", nil
	}

	name, hello := readEvent()
	if name != "hello" || hello["seq"].(float64) < 1 {
		t.Fatalf("hello event: %s %v", name, hello)
	}

	// Commit a batch and expect its notification.
	created := e.call("POST", "/api/v1/projects", map[string]any{"name": "notify me"}, 201)
	name, note := readEvent()
	if name != "commit" {
		t.Fatalf("event name %q", name)
	}
	if int64(note["seq"].(float64)) != int64(created["version"].(float64)) {
		t.Fatalf("commit seq %v, created version %v", note["seq"], created["version"])
	}
	types := note["types"].([]any)
	if len(types) != 1 || types[0].(string) != "project.created" {
		t.Fatalf("commit types: %v", types)
	}
	if str(note, "batch") == "" || str(note, "ts") == "" {
		t.Fatalf("commit note incomplete: %v", note)
	}
}

// TestSSESlowSubscriberNeverBlocksCommits: the hub's notify is non-blocking
// by contract (it runs on the writer goroutine) — a subscriber that never
// reads must not stall commits once its buffer overflows. Well past the
// buffer size of commits complete under a generous deadline.
func TestSSESlowSubscriberNeverBlocksCommits(t *testing.T) {
	e := newEnv(t)
	ch := e.s.hub.subscribe() // deliberately never read
	defer e.s.hub.unsubscribe(ch)

	const commits = 40 // buffer is 16: most notifications must be dropped
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < commits; i++ {
			e.call("POST", "/api/v1/projects", map[string]any{"name": "spam"}, 201)
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("commits stalled behind an unread SSE subscriber")
	}
	if got := len(ch); got != cap(ch) {
		t.Fatalf("subscriber buffer holds %d, want full (%d) with the rest dropped", got, cap(ch))
	}
}

// TestSSEShutdownEndsStream: Server.Shutdown ends open streams so the HTTP
// server can drain.
func TestSSEShutdownEndsStream(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", e.ts.URL+"/api/v1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				done <- err
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond) // let the handler subscribe
	e.s.Shutdown()
	select {
	case <-done: // EOF: the stream ended
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end on Shutdown")
	}
}
