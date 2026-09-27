// Package store keeps modules' data on a node: records of the entity types
// they declare, and key-value pairs, each module's apart from the others'.
// Drivers are compiled into the node; SQLite is the first. See
// spec/modules.md, "Storage".
package store

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
)

// Driver stores modules' records and key-value pairs.
type Driver interface {
	// Index lets records of entity be queried and sorted by field.
	Index(ctx context.Context, module, entity, field string) error
	Put(ctx context.Context, module, entity, id string, record json.RawMessage) error
	// Get returns ErrNotFound if there's no such record.
	Get(ctx context.Context, module, entity, id string) (json.RawMessage, error)
	Delete(ctx context.Context, module, entity, id string) error
	// Query returns the records that match q, which may only use indexed
	// fields, and id.
	Query(ctx context.Context, module, entity string, q Query) ([]json.RawMessage, error)

	// KVGet returns ErrNotFound if there's no such key.
	KVGet(ctx context.Context, module, key string) (json.RawMessage, error)
	KVPut(ctx context.Context, module, key string, value json.RawMessage) error
	KVDelete(ctx context.Context, module, key string) error
	// KVList returns the pairs whose key starts with prefix, by key.
	KVList(ctx context.Context, module, prefix string, limit int) ([]Pair, error)

	Close() error
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

// Pair is a key and its value.
type Pair struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

var ErrNotFound = errors.New("not found")

// Field matches the names of fields that can be indexed: top-level ones.
var Field = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// operators maps query conditions to SQL.
var operators = map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}
