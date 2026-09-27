// Package store keeps modules' data on a node, in a namespace per module and
// store. There are kinds of stores, each with an interface of its own:
// records of the entity types modules declare (EntityStore, entity_store.go)
// and key-value pairs (KVStore, kv_store.go); blobs and more will follow. A
// driver, in a package of its own such as sqlite, implements the kinds it
// supports and registers itself, and a node binds each store a module
// declares to a driver that supports its kind. Drivers are compiled into the
// node. See spec/modules.md, "Storage".
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"gopkg.in/yaml.v3"
)

// Kinds of stores, as modules declare them.
const (
	KindEntity = "entity"
	KindKV     = "kv"
)

// Kinds lists the kinds there are.
var Kinds = []string{KindEntity, KindKV}

// Config configures a store, as a node definition declares it: which driver,
// and the options that driver takes, e.g. path for sqlite.
type Config struct {
	Driver  string
	Options Options
}

// Options are a driver's options, as the node definition gives them.
type Options map[string]any

// String returns the option called name as a string, "" if it isn't set.
func (o Options) String(name string) string {
	s, _ := o[name].(string)
	return s
}

// UnmarshalYAML reads a store's block: driver, and the rest as options.
func (c *Config) UnmarshalYAML(n *yaml.Node) error {
	var m map[string]any
	if err := n.Decode(&m); err != nil {
		return err
	}
	driver, _ := m["driver"].(string)
	delete(m, "driver")
	*c = Config{Driver: driver, Options: m}
	return nil
}

// A Store is an open database. It implements the kinds it supports, see
// Entities and KV.
type Store interface {
	Close() error
}

// An Opener opens a driver's store. Relative paths are under dataDir; an
// empty dataDir keeps every store in memory.
type Opener func(cfg Config, dataDir string) (Store, error)

var (
	driversMu sync.RWMutex
	drivers   = map[string]Opener{}
)

// Register makes a driver available to Open, by name. Driver packages call
// it from init, so a node has the drivers it imports.
func Register(driver string, open Opener) {
	driversMu.Lock()
	defer driversMu.Unlock()
	if _, dup := drivers[driver]; dup {
		panic("store: driver " + driver + " registered twice")
	}
	drivers[driver] = open
}

// Open opens the store cfg configures, with its driver.
func Open(cfg Config, dataDir string) (Store, error) {
	if cfg.Driver == "" {
		cfg.Driver = "sqlite"
	}
	driversMu.RLock()
	open := drivers[cfg.Driver]
	driversMu.RUnlock()
	if open == nil {
		return nil, fmt.Errorf("unknown store driver %q", cfg.Driver)
	}
	return open(cfg, dataDir)
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

// Any, as the version a write requires, makes it unconditional. Otherwise a
// write only happens if the record or pair has that version, where 0 means
// there is none yet; else it fails with a *Conflict.
const Any int64 = -1

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
