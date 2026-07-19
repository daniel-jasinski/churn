// Package interchange implements the canonical JSONL view of the event log
// (DESIGN.md §5.4): Export streams stored envelopes as byte-stable JSONL,
// one envelope per line ordered by seq; Import restores such a stream into
// an empty data directory after full envelope-hygiene and domain validation.
//
// Byte stability is by construction, never by re-serialization. Each line is
// assembled from three byte sources with fixed provenance:
//
//   - integers (seq, v) — strconv.AppendInt;
//   - strings (id, origin, batch, causes, ts, actor, type, entity) —
//     canonjson's string escaping, the same escaper payloads go through;
//   - data — a verbatim copy of the stored payload bytes, which were
//     canonicalized exactly once when they entered a log (by the writer at
//     append time, or verified canonical by Import on restore).
//
// Field order is fixed by AppendEnvelope to the documented §5.2 envelope
// shape, so export → import → export reproduces identical bytes.
package interchange

import (
	"fmt"
	"io"
	"strconv"

	"churn/internal/canonjson"
	"churn/internal/event"
	"churn/internal/store"
)

// AppendEnvelope appends the canonical single-line JSON form of ev to dst
// and returns the extended slice, without a trailing newline. The field
// order is the documented envelope shape (§5.2): seq, id, origin, batch,
// causes, ts, actor, type, v, entity, data. ev.Data is copied verbatim and
// must already be canonical JSON.
func AppendEnvelope(dst []byte, ev event.Envelope) ([]byte, error) {
	if len(ev.Data) == 0 {
		return nil, fmt.Errorf("interchange: event %s has no payload", ev.ID)
	}
	var err error
	str := func(field, s string) []byte {
		if err != nil {
			return dst
		}
		dst = append(dst, ',', '"')
		dst = append(dst, field...)
		dst = append(dst, '"', ':')
		var b []byte
		if b, err = canonjson.Encode(s); err == nil {
			dst = append(dst, b...)
		}
		return dst
	}
	dst = append(dst, `{"seq":`...)
	dst = strconv.AppendInt(dst, ev.Seq, 10)
	dst = str("id", ev.ID)
	dst = str("origin", ev.Origin)
	dst = str("batch", ev.Batch)
	if ev.Causes == nil {
		dst = append(dst, `,"causes":null`...)
	} else {
		dst = str("causes", *ev.Causes)
	}
	dst = str("ts", ev.TS)
	dst = str("actor", ev.Actor)
	dst = str("type", ev.Type)
	dst = append(dst, `,"v":`...)
	dst = strconv.AppendInt(dst, int64(ev.V), 10)
	dst = str("entity", ev.Entity)
	if err != nil {
		return nil, fmt.Errorf("interchange: encoding envelope %s: %w", ev.ID, err)
	}
	dst = append(dst, `,"data":`...)
	dst = append(dst, ev.Data...)
	dst = append(dst, '}')
	return dst, nil
}

// Export streams the whole event log as canonical JSONL to w: one envelope
// per line, ordered by seq. Payload bytes are copied from the store, never
// re-serialized (§5.4). Export may run against a live server: the store's
// Scan sees a consistent complete-batch snapshot under WAL.
func Export(st *store.Store, w io.Writer) error {
	buf := make([]byte, 0, 512)
	return st.Scan(func(ev event.Envelope) error {
		var err error
		buf, err = AppendEnvelope(buf[:0], ev)
		if err != nil {
			return err
		}
		buf = append(buf, '\n')
		if _, err := w.Write(buf); err != nil {
			return fmt.Errorf("interchange: writing export: %w", err)
		}
		return nil
	})
}
