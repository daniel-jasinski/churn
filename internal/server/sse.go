package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"churn/internal/writer"
)

// SSE commit notifications — a phase-3 feature (DESIGN.md §6 "SSE live
// refresh") landed early because the writer's commit hook is the natural
// seam and M6's UI stays trivially fresh with it.
//
// GET /api/v1/events/stream emits one "hello" event with the current log
// position, then one "commit" event per committed batch:
//
//	event: commit
//	data: {"seq":123,"batch":"01J…","ts":"…","types":["thing.created"]}
//
// Notifications are best-effort refresh hints, not a replicated log: a slow
// consumer's overflowed notifications are dropped (its next fetch reads the
// projection anyway), and clients must treat any gap in seq as "refetch".

// commitNote is the wire payload of one commit notification.
type commitNote struct {
	Seq   int64    `json:"seq"`
	Batch string   `json:"batch"`
	TS    string   `json:"ts"`
	Types []string `json:"types"`
}

// sseHub fans writer commit notifications out to subscribers.
type sseHub struct {
	mu   sync.Mutex
	subs map[chan commitNote]struct{}
}

func newSSEHub() *sseHub {
	return &sseHub{subs: map[chan commitNote]struct{}{}}
}

// notify is the writer's commit hook: it runs on the writer goroutine and
// must not block — full subscriber buffers are skipped.
func (h *sseHub) notify(info writer.CommitInfo) {
	note := commitNote{Seq: info.Seq, Batch: info.Batch, TS: info.TS, Types: info.Types}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- note:
		default: // slow consumer: drop; the seq gap tells it to refetch
		}
	}
}

func (h *sseHub) subscribe() chan commitNote {
	ch := make(chan commitNote, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *sseHub) unsubscribe(ch chan commitNote) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// getEventStream implements GET /api/v1/events/stream.
func (s *Server) getEventStream(rw http.ResponseWriter, r *http.Request) {
	flusher, ok := rw.(http.Flusher)
	if !ok {
		writeError(rw, &apiError{status: http.StatusInternalServerError,
			kind: "internal", message: "response writer does not support streaming"})
		return
	}
	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusOK)

	p := s.w.Projection()
	if err := writeSSE(rw, "hello", commitNote{Seq: p.LastSeq, Batch: p.LastBatch, TS: p.LastTS, Types: []string{}}); err != nil {
		return
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.quit: // server shutting down: end the stream so it can drain
			return
		case note := <-ch:
			if err := writeSSE(rw, "commit", note); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(rw http.ResponseWriter, event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, b)
	return err
}
