// Package store keeps modules' data on a node, in a namespace per module and
// store. There are kinds of stores, each with an interface of its own:
// records of the entity types modules declare (EntityStore), and key-value
// pairs (KVStore). A driver implements the kinds it supports, and a node
// binds each store a module declares to a driver that supports its kind.
// Drivers are compiled into the node; SQLite is the first. See
// spec/modules.md, "Storage".
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// Kinds of stores, as modules declare them.
const (
	KindEntity = "entity"
	KindKV     = "kv"
)

// Kinds lists the kinds there are.
var Kinds = []string{KindEntity, KindKV}

// Config configures a store, as a node definition declares it.
type Config struct {
	// Driver is the kind of database: "sqlite".
	Driver string `yaml:"driver" json:"driver"`
	// Path is where SQLite keeps the database: a file, relative to the
	// node's data directory unless absolute, or ":memory:".
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
}

// Open opens the store cfg configures. Relative paths are under dataDir; an
// empty dataDir keeps every store in memory.
func Open(cfg Config, dataDir string) (Store, error) {
	switch cfg.Driver {
	case "sqlite", "":
		return OpenSQLite(sqlitePath(cfg.Path, dataDir))
	}
	return nil, fmt.Errorf("unknown store driver %q", cfg.Driver)
}

// A Store is an open database. It implements the kinds it supports, see
// Entities and KV.
type Store interface {
	Close() error
}

// Entities returns s as a store of records, if it is one.
func Entities(s Store) (EntityStore, bool) {
	e, ok := s.(interface{ Entities() EntityStore })
	if !ok {
		return nil, false
	}
	return e.Entities(), true
}

// KV returns s as a store of key-value pairs, if it is one.
func KV(s Store) (KVStore, bool) {
	k, ok := s.(interface{ KV() KVStore })
	if !ok {
		return nil, false
	}
	return k.KV(), true
}

// Supports reports whether s is a store of the given kind.
func Supports(s Store, kind string) bool {
	switch kind {
	case KindEntity:
		_, ok := Entities(s)
		return ok
	case KindKV:
		_, ok := KV(s)
		return ok
	}
	return false
}

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

// A KVStore keeps key-value pairs, each namespace's apart from the others'.
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

// Any, as the version a write requires, makes it unconditional. Otherwise a
// write only happens if the record or pair has that version, where 0 means
// there is none yet; else it fails with a *Conflict.
const Any int64 = -1

// Record is a record with its ID and version, which counts its writes from
// 1.
type Record struct {
	ID      string          `json:"id"`
	Version int64           `json:"version"`
	Data    json.RawMessage `json:"record"`
}

// Pair is a key and its value, with the version of the value.
type Pair struct {
	Key     string          `json:"key"`
	Version int64           `json:"version"`
	Value   json.RawMessage `json:"value"`
}

// EntityOp is one write of a batch of records: a put if Record is set, else
// a delete.
type EntityOp struct {
	Entity, ID string
	Record     json.RawMessage
	IfVersion  int64
}

// KVOp is one write of a batch of pairs: a put if Value is set, else a
// delete.
type KVOp struct {
	Key       string
	Value     json.RawMessage
	IfVersion int64
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

var ErrNotFound = errors.New("not found")

// ErrConflict is what a *Conflict is.
var ErrConflict = errors.New("version conflict")

// Conflict is why a conditional write didn't happen: the record or pair has
// another version than required. Have is 0 if there is none.
type Conflict struct {
	Want, Have int64
}

func (c *Conflict) Error() string {
	switch {
	case c.Have == 0:
		return fmt.Sprintf("version conflict: want %d, but there is none", c.Want)
	case c.Want == 0:
		return fmt.Sprintf("version conflict: want none, have %d", c.Have)
	}
	return fmt.Sprintf("version conflict: want %d, have %d", c.Want, c.Have)
}

func (c *Conflict) Is(target error) bool { return target == ErrConflict }

// Field matches the names of fields that can be indexed: top-level ones.
var Field = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// operators maps query conditions to SQL.
var operators = map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}

// Values are a namespace's key-value pairs as Go values, for the node's own
// parts.
type Values struct {
	kv KVStore
	ns string
}

// NewValues returns the pairs of ns in kv, as Go values.
func NewValues(kv KVStore, ns string) Values { return Values{kv, ns} }

// Get decodes the value of key into v. It fails with ErrNotFound if there's
// none.
func (vs Values) Get(ctx context.Context, key string, v any) error {
	p, err := vs.kv.Get(ctx, vs.ns, key)
	if err != nil {
		return err
	}
	return json.Unmarshal(p.Value, v)
}

func (vs Values) Put(ctx context.Context, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = vs.kv.Put(ctx, vs.ns, key, data, Any)
	return err
}
