package store

import (
	"database/sql"
	"fmt"
	"strings"

	"churn/internal/event"
)

// HistoryFilter selects a slice of the event log for the history API
// (DESIGN.md §5.1). Zero values mean "no constraint"; conditions combine
// with AND. All filtering happens at the database level against the indexed
// envelope columns (§5.2 "where the database answers queries") — the store
// filters WHICH events are returned and never interprets them.
type HistoryFilter struct {
	// Entity matches events whose envelope entity column is the id OR that
	// reference the id through the derived event_refs table (multi-entity
	// events: a dependency's two things, an allocation's thing, resource,
	// and requirement).
	Entity string
	// Type matches the envelope type column exactly.
	Type string
	// Actor matches the envelope actor column exactly.
	Actor string
	// Batch matches the envelope batch column exactly.
	Batch string
	// SinceSeq / UntilSeq bound the seq range inclusively; 0 means unbounded.
	SinceSeq int64
	UntilSeq int64
	// Limit caps the number of returned events; <= 0 means unlimited.
	Limit int
}

// History streams the events selected by f to fn in seq order, reading on
// the read pool (a consistent WAL snapshot; never blocks the writer). A
// non-nil error from fn aborts the scan and is returned.
func (s *Store) History(f HistoryFilter, fn func(event.Envelope) error) error {
	q := `SELECT seq, id, origin, batch, causes, ts, actor, type, v, entity, data FROM events`
	var conds []string
	var args []any
	if f.Entity != "" {
		conds = append(conds,
			`(entity = ? OR seq IN (SELECT event_seq FROM event_refs WHERE entity_id = ?))`)
		args = append(args, f.Entity, f.Entity)
	}
	if f.Type != "" {
		conds = append(conds, `type = ?`)
		args = append(args, f.Type)
	}
	if f.Actor != "" {
		conds = append(conds, `actor = ?`)
		args = append(args, f.Actor)
	}
	if f.Batch != "" {
		conds = append(conds, `batch = ?`)
		args = append(args, f.Batch)
	}
	if f.SinceSeq > 0 {
		conds = append(conds, `seq >= ?`)
		args = append(args, f.SinceSeq)
	}
	if f.UntilSeq > 0 {
		conds = append(conds, `seq <= ?`)
		args = append(args, f.UntilSeq)
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY seq`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := s.rdb.Query(q, args...)
	if err != nil {
		return fmt.Errorf("store: history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var ev event.Envelope
		var causes, entity sql.NullString
		var data string
		if err := rows.Scan(&ev.Seq, &ev.ID, &ev.Origin, &ev.Batch, &causes,
			&ev.TS, &ev.Actor, &ev.Type, &ev.V, &entity, &data); err != nil {
			return fmt.Errorf("store: history row: %w", err)
		}
		if causes.Valid {
			ev.Causes = &causes.String
		}
		ev.Entity = entity.String
		ev.Data = []byte(data)
		if err := fn(ev); err != nil {
			return err
		}
	}
	return rows.Err()
}
