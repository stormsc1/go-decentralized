package store

import (
	"context"
	"encoding/json"
	"regexp"
)

// An EntityStore keeps records of entity types, each namespace's apart from
// the others'. Writes take the version the record must have, see Any.
type EntityStore interface {
	// Index lets records of entity be queried and sorted by field.
	Index(ctx context.Context, ns, entity, field string) error
	// Put stores record, replacing any with the same ID, and returns its
	// new version.
	Put(ctx context.Context, ns, entity, id string, record json.RawMessage, ifVersion int64) (int64, error)
	// Get returns ErrNotFound if there's no such record.
	Get(ctx context.Context, ns, entity, id string) (Record, error)
	Delete(ctx context.Context, ns, entity, id string, ifVersion int64) error
	// Query returns the records that match q, which may only use indexed
	// fields, and id.
	Query(ctx context.Context, ns, entity string, q Query) ([]Record, error)
	// Batch applies ops in order, all or none, and returns the new version
	// of each put, 0 for deletes.
	Batch(ctx context.Context, ns string, ops []EntityOp) ([]int64, error)
}

// Record is a record with its ID and version, which counts its writes from
// 1.
type Record struct {
	ID      string          `json:"id"`
	Version int64           `json:"version"`
	Data    json.RawMessage `json:"record"`
}

// EntityOp is one write of a batch of records: a put if Record is set, else
// a delete.
type EntityOp struct {
	Entity, ID string
	Record     json.RawMessage
	IfVersion  int64
}

// Query selects records.
type Query struct {
	// Where maps fields to the value they must have, or to conditions:
	// {"gt": v}, {"gte": v}, {"lt": v} or {"lte": v}.
	Where map[string]any `json:"where,omitempty"`
	// OrderBy is the field to sort by. Ties, and records by default, sort by
	// id.
	OrderBy string `json:"order_by,omitempty"`
	Desc    bool   `json:"desc,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// Field matches the names of fields that can be indexed: top-level ones.
var Field = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
