package server

import (
	"net/http"
	"strconv"

	"churn/internal/event"
	"churn/internal/interchange"
	"churn/internal/store"
)

// getHistory implements GET /api/v1/history — the §5.1 audit trail, filtered
// at the DATABASE level against the indexed envelope columns (never the
// projection; §5.2 "where the database answers queries"):
//
//	entity=     envelope entity column OR an event_refs hit (multi-entity
//	            events: a dependency's things, an allocation's triple)
//	type=       exact event type
//	actor=      exact actor
//	batch=      exact batch id
//	since_seq=  inclusive lower seq bound
//	until_seq=  inclusive upper seq bound
//	limit=      max events (default and absent: unlimited)
//	format=     json (default): {"events": [envelope…]}
//	            jsonl: canonical envelope lines (application/x-ndjson),
//	            byte-identical to export-log for the same rows
//
// Events stream in seq order.
func (s *Server) getHistory(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.HistoryFilter{
		Entity: q.Get("entity"),
		Type:   q.Get("type"),
		Actor:  q.Get("actor"),
		Batch:  q.Get("batch"),
	}
	var e *apiError
	if f.SinceSeq, e = parseIntParam(q.Get("since_seq"), "since_seq"); e != nil {
		writeError(rw, e)
		return
	}
	if f.UntilSeq, e = parseIntParam(q.Get("until_seq"), "until_seq"); e != nil {
		writeError(rw, e)
		return
	}
	limit, e := parseIntParam(q.Get("limit"), "limit")
	if e != nil {
		writeError(rw, e)
		return
	}
	f.Limit = int(limit)

	switch q.Get("format") {
	case "", "json":
		events := []event.Envelope{}
		if err := s.st.History(f, func(ev event.Envelope) error {
			events = append(events, ev)
			return nil
		}); err != nil {
			writeError(rw, mapError(err))
			return
		}
		writeJSON(rw, http.StatusOK, struct {
			Events []event.Envelope `json:"events"`
		}{events})

	case "jsonl":
		rw.Header().Set("Content-Type", "application/x-ndjson")
		buf := make([]byte, 0, 512)
		if err := s.st.History(f, func(ev event.Envelope) error {
			var err error
			buf, err = interchange.AppendEnvelope(buf[:0], ev)
			if err != nil {
				return err
			}
			buf = append(buf, '\n')
			_, err = rw.Write(buf)
			return err
		}); err != nil {
			// Headers are committed once the first line is out; a later
			// failure can only truncate the stream.
			s.logger.Printf("history jsonl stream: %v", err)
		}

	default:
		writeError(rw, errBadRequest("format %q: want json or jsonl", q.Get("format")))
	}
}

// parseIntParam parses a non-negative integer query parameter; empty is 0.
func parseIntParam(v, name string) (int64, *apiError) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, errBadRequest("%s %q: want a non-negative integer", name, v)
	}
	return n, nil
}
