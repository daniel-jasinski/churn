// Package event defines the event envelope (DESIGN.md §5.2) and the typed
// event catalog: a registry keyed by (type, version) that decodes and
// validates payloads.
//
// Fail-closed rule: an unknown event type or an unsupported payload version
// is an error — a reader must halt rather than fold a plausible-but-wrong
// projection. Unknown payload *fields* are tolerated (encoding/json ignores
// them), so payloads can grow compatibly.
//
// This package performs no I/O and touches no clock; it is imported by the
// pure domain core.
package event

import (
	"encoding/json"
	"fmt"
	"time"
)

// TSFormat is the writer's fixed-width UTC timestamp layout, the only form
// an envelope ts may take. Fixed width means lexicographic order is
// chronological order, which the store's ev_ts index, the fold's
// monotonicity check, and the writer's monotone clamp all rely on — a
// foreign-width ts imported into a log would poison that clamp forever.
const TSFormat = "2006-01-02T15:04:05.000Z"

// ValidTS reports whether s is exactly a TSFormat timestamp: it must parse
// under the layout AND re-format to the identical bytes, so width variants
// (e.g. seconds precision) and non-canonical spellings are rejected.
func ValidTS(s string) bool {
	t, err := time.Parse(TSFormat, s)
	return err == nil && t.UTC().Format(TSFormat) == s
}

// Envelope is the uniform shape of every log entry, identical in the SQLite
// row and the JSONL export.
type Envelope struct {
	// Seq is the position in the log; assigned by the store on append.
	Seq int64 `json:"seq"`
	// ID is a ULID: globally unique, lexically time-sortable.
	ID string `json:"id"`
	// Origin is the id of the writer lineage that appended the event.
	Origin string `json:"origin"`
	// Batch groups the events of one atomic domain operation.
	Batch string `json:"batch"`
	// Causes optionally targets the id of a specific prior event.
	// Reserved — always nil in V1.
	Causes *string `json:"causes"`
	// TS is the writer-assigned timestamp, monotone with Seq.
	TS string `json:"ts"`
	// Actor is the acting user.
	Actor string `json:"actor"`
	// Type names the event, e.g. "log.initialized".
	Type string `json:"type"`
	// V is the payload schema version.
	V int `json:"v"`
	// Entity is the primary entity the event is about; may be empty.
	Entity string `json:"entity"`
	// Data is the canonicalized JSON payload.
	Data json.RawMessage `json:"data"`
}

// Event type names in the log namespace.
const (
	TypeLogInitialized = "log.initialized"
	TypeWriterStarted  = "writer.started"
)

// LogInitialized is the mandatory first event of every log (v1). It records
// the immutable workspace id; the envelope's origin is the first writer
// lineage.
type LogInitialized struct {
	WorkspaceID string `json:"workspace_id"`
}

// Validate implements the payload contract.
func (p *LogInitialized) Validate() error {
	if p.WorkspaceID == "" {
		return fmt.Errorf("log.initialized: workspace_id must not be empty")
	}
	return nil
}

// WriterStarted marks a new writer lineage (v1). The fresh lineage id is the
// envelope's origin; the payload carries nothing.
type WriterStarted struct{}

// Validate implements the payload contract.
func (p *WriterStarted) Validate() error { return nil }

// Payload is a decoded, validated event payload.
type Payload interface {
	// Validate reports whether the payload is well-formed in isolation
	// (cross-entity rules live in the domain fold).
	Validate() error
}

// Ref names an entity a payload references beyond the envelope's own entity
// column, with the role it plays. Refs feed the store's derived event_refs
// side table, one row per (event, referenced entity, role).
type Ref struct {
	Entity string
	Role   string
}

// Referencer is implemented by payloads that reference entities beyond the
// envelope's entity column. Refs must be derivable from the payload alone —
// the event_refs table is rebuilt from events only (churn reindex).
type Referencer interface {
	// Refs lists the referenced entities in a fixed, deterministic order.
	Refs() []Ref
}

// RefsOf decodes and validates the payload of an event of the given type and
// version and returns its referenced entities — the single event_refs
// derivation shared by live appends, import-log restore, and reindex, so the
// derived side table can never diverge across write paths. Events whose
// payloads reference nothing return nil.
func RefsOf(typ string, v int, data []byte) ([]Ref, error) {
	p, err := Decode(typ, v, data)
	if err != nil {
		return nil, err
	}
	if r, ok := p.(Referencer); ok {
		return r.Refs(), nil
	}
	return nil, nil
}

// entry describes one registered (type, v).
type entry struct {
	// dec builds an empty payload value to decode into.
	dec func() Payload
	// entityPrefix is the required typed prefix of the envelope's entity
	// column ("" for events that carry no entity).
	entityPrefix string
}

// registry maps (type, v) to its entry. Populated at package init only;
// read-only afterwards, so concurrent reads are safe.
var registry = map[key]entry{
	{TypeLogInitialized, 1}: {dec: func() Payload { return new(LogInitialized) }},
	{TypeWriterStarted, 1}:  {dec: func() Payload { return new(WriterStarted) }},
}

type key struct {
	typ string
	v   int
}

// Known reports whether (typ, v) is a registered event type and version.
func Known(typ string, v int) bool {
	_, ok := registry[key{typ, v}]
	return ok
}

// EntityPrefix returns the typed id prefix the envelope's entity column must
// carry for a registered (typ, v) — "" for events without an entity — and
// whether (typ, v) is registered at all.
func EntityPrefix(typ string, v int) (string, bool) {
	e, ok := registry[key{typ, v}]
	return e.entityPrefix, ok
}

// Decode decodes and validates the payload of an event of the given type and
// version. Unknown type or version fails closed; unknown fields in data are
// tolerated.
func Decode(typ string, v int, data []byte) (Payload, error) {
	e, ok := registry[key{typ, v}]
	if !ok {
		return nil, fmt.Errorf("event: unknown event type %q v%d: refusing to interpret", typ, v)
	}
	p := e.dec()
	if err := json.Unmarshal(data, p); err != nil {
		return nil, fmt.Errorf("event: decoding %s v%d payload: %w", typ, v, err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("event: invalid %s v%d payload: %w", typ, v, err)
	}
	return p, nil
}
