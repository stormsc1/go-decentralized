package store

import (
	"context"
	"encoding/json"
)

// A KVStore keeps key-value pairs, each namespace's apart from the others'.
// Writes take the version the pair must have, see Any.
type KVStore interface {
	// Get returns ErrNotFound if there's no such key.
	Get(ctx context.Context, ns, key string) (Pair, error)
	Put(ctx context.Context, ns, key string, value json.RawMessage, ifVersion int64) (int64, error)
	Delete(ctx context.Context, ns, key string, ifVersion int64) error
	// List returns the pairs whose key starts with prefix, by key.
	List(ctx context.Context, ns, prefix string, limit int) ([]Pair, error)
	// Batch applies ops in order, all or none, and returns the new version
	// of each put, 0 for deletes.
	Batch(ctx context.Context, ns string, ops []KVOp) ([]int64, error)
}

// Pair is a key and its value, with the version of the value.
type Pair struct {
	Key     string          `json:"key"`
	Version int64           `json:"version"`
	Value   json.RawMessage `json:"value"`
}

// KVOp is one write of a batch of pairs: a put if Value is set, else a
// delete.
type KVOp struct {
	Key       string
	Value     json.RawMessage
	IfVersion int64
}
