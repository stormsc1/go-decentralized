package node

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"go-decentralized/internal/store"
	_ "go-decentralized/internal/store/sqlite" // the drivers compiled into the node
	"go-decentralized/module"
)

//go:embed store.module.yaml
var storeManifest []byte

// localStore is the node's own store, for its built-in parts: the only store
// a node definition needn't declare. Modules can't bind to it, so a pool's
// nodes never share it.
const localStore = "local"

// defaultLimit is how many records or pairs a query returns, unless it says.
const defaultLimit = 100

// entityType is one of a module's entity types, as the node stores them.
type entityType struct {
	schema  *jsonschema.Schema
	indexes []string
}

// openStores opens the stores the node definition declares, and local,
// which every node has. SQLite stores without a path are files in the data
// directory, named after the node and the store.
func (n *Node) openStores() error {
	n.stores = map[string]store.Store{}
	for name, cfg := range n.Config.Stores {
		if name == "" {
			return errors.New("a store has no name")
		}
		if cfg.Path == "" && (cfg.Driver == "" || cfg.Driver == "sqlite") {
			cfg.Path = n.Config.Name + "." + name + ".db"
		}
		s, err := store.Open(cfg, n.Config.DataDir)
		if err != nil {
			return fmt.Errorf("store %s: %w", name, err)
		}
		n.stores[name] = s
	}
	if n.stores[localStore] == nil {
		s, err := store.Open(store.Config{Path: n.Config.Name + ".local.db"}, n.Config.DataDir)
		if err != nil {
			return fmt.Errorf("store %s: %w", localStore, err)
		}
		n.stores[localStore] = s
	}
	return nil
}

// bindStores binds the stores a module declares to the node's, as the node
// definition says, checks each keeps that kind of data, and prepares its
// entity types: their schemas compiled and their indexes made. n.mu must be
// held.
func (n *Node) bindStores(l *loaded) (map[string]entityType, error) {
	m := l.manifest
	l.stores = map[string]binding{}
	entities := map[string]entityType{}
	for _, ds := range m.Stores {
		name, ok := l.bindings[ds.Name]
		if !ok {
			return nil, fmt.Errorf("%s: store %s isn't bound: bind it to one of the node's stores in the node definition", m.Name, ds.Name)
		}
		if name == localStore {
			return nil, fmt.Errorf("%s: store %s is bound to %s, which is the node's own", m.Name, ds.Name, localStore)
		}
		s := n.stores[name]
		if s == nil {
			return nil, fmt.Errorf("%s: store %s is bound to %q, which the node doesn't have", m.Name, ds.Name, name)
		}
		if !store.Supports(s, ds.Type) {
			return nil, fmt.Errorf("%s: store %s is bound to %s, which keeps no %s data", m.Name, ds.Name, name, ds.Type)
		}
		l.stores[ds.Name] = binding{store: name, kind: ds.Type}
		ns := m.Name + "/" + ds.Name
		for _, e := range ds.Entities {
			schema, err := compile(m, e.Schema)
			if err != nil {
				return nil, fmt.Errorf("%s entity %s: schema: %w", m.Name, e.Name, err)
			}
			records, _ := store.Entities(s)
			for _, field := range e.Indexes {
				if err := records.Index(context.Background(), ns, e.Name, field); err != nil {
					return nil, fmt.Errorf("%s entity %s: index %s: %w", m.Name, e.Name, field, err)
				}
			}
			entities[ns+"/"+e.Name] = entityType{schema: schema, indexes: e.Indexes}
		}
	}
	for name := range l.bindings {
		if _, ok := m.Store(name); !ok {
			return nil, fmt.Errorf("%s: the node definition binds store %q, which the module doesn't declare", m.Name, name)
		}
	}
	return entities, nil
}

// local returns the node's own store, for its built-in parts, as pairs of
// Go values in the namespace ns.
func (n *Node) local(ns string) store.Values {
	kv, _ := store.KV(n.stores[localStore])
	return store.NewValues(kv, ns)
}

// storeHandlers handle the store's capabilities, see store.module.yaml. They
// keep each module's data apart: a module only reaches its own.
func (n *Node) storeHandlers() map[string]module.Handler {
	return map[string]module.Handler{
		"put":       module.HandlerFor(n.storePut),
		"get":       module.HandlerFor(n.storeGet),
		"delete":    module.HandlerFor(n.storeDelete),
		"query":     module.HandlerFor(n.storeQuery),
		"batch":     module.HandlerFor(n.storeBatch),
		"kv_get":    module.HandlerFor(n.kvGet),
		"kv_put":    module.HandlerFor(n.kvPut),
		"kv_delete": module.HandlerFor(n.kvDelete),
		"kv_list":   module.HandlerFor(n.kvList),
		"kv_batch":  module.HandlerFor(n.kvBatch),
	}
}

// resolve returns the calling module's store called name, of the given
// kind: the module, the namespace its data is in, and the node's store. An
// empty name means the module's only store of that kind.
func (n *Node) resolve(ctx context.Context, name, kind string) (*loaded, string, store.Store, error) {
	owner := callingModule(ctx)
	if owner == "" {
		return nil, "", nil, module.Errorf(module.CodePermissionDenied, "only modules have stores")
	}
	l := n.module(owner)
	if l == nil {
		return nil, "", nil, module.Errorf(module.CodeNotFound, "no module %s", owner)
	}
	if name == "" {
		for sn, b := range l.stores {
			if b.kind != kind {
				continue
			}
			if name != "" {
				return nil, "", nil, module.Errorf(module.CodeInvalidArgument, "module %s has several %s stores: say which", owner, kind)
			}
			name = sn
		}
		if name == "" {
			return nil, "", nil, module.Errorf(module.CodeNotFound, "module %s declares no %s store", owner, kind)
		}
	}
	b, ok := l.stores[name]
	if !ok || b.kind != kind {
		return nil, "", nil, module.Errorf(module.CodeNotFound, "module %s has no %s store %q", owner, kind, name)
	}
	return l, owner + "/" + name, n.stores[b.store], nil
}

// records returns the calling module's entity store called name, and its
// namespace.
func (n *Node) recordsOf(ctx context.Context, name string) (*loaded, string, store.EntityStore, error) {
	l, ns, s, err := n.resolve(ctx, name, store.KindEntity)
	if err != nil {
		return nil, "", nil, err
	}
	records, _ := store.Entities(s) // checked when the module was bound
	return l, ns, records, nil
}

// pairsOf returns the calling module's key-value store called name, and its
// namespace.
func (n *Node) pairsOf(ctx context.Context, name string) (string, store.KVStore, error) {
	_, ns, s, err := n.resolve(ctx, name, store.KindKV)
	if err != nil {
		return "", nil, err
	}
	pairs, _ := store.KV(s)
	return ns, pairs, nil
}

// entity returns the entity type of the calling module's store.
func (n *Node) entity(l *loaded, ns, name string) (entityType, error) {
	n.mu.RLock()
	t, ok := n.entities[ns+"/"+name]
	n.mu.RUnlock()
	if !ok {
		return entityType{}, module.Errorf(module.CodeNotFound, "module %s has no entity type %q there", l.manifest.Name, name)
	}
	return t, nil
}

// storeError turns a driver's error into the caller's.
func storeError(err error, what string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return module.Errorf(module.CodeNotFound, "no %s", what)
	case errors.Is(err, store.ErrConflict):
		return module.Errorf(module.CodeConflict, "%s: %v", what, err)
	}
	return err
}

// storeRef names one of the calling module's stores: its own name for it,
// or none for its only one of the kind.
type storeRef struct {
	Store string `json:"store"`
}

type recordRef struct {
	storeRef
	Entity string `json:"entity"`
	ID     string `json:"id"`
}

// condition is the version a write requires, if it does.
type condition struct {
	IfVersion *int64 `json:"if_version"`
}

func (c condition) version() int64 {
	if c.IfVersion == nil {
		return store.Any
	}
	return *c.IfVersion
}

type versionOutput struct {
	Version int64 `json:"version"`
}

type versionsOutput struct {
	Versions []int64 `json:"versions"`
}

func (n *Node) storePut(ctx context.Context, in struct {
	recordRef
	condition
	Record json.RawMessage `json:"record"`
}) (versionOutput, error) {
	l, ns, records, err := n.recordsOf(ctx, in.Store)
	if err != nil {
		return versionOutput{}, err
	}
	t, err := n.entity(l, ns, in.Entity)
	if err != nil {
		return versionOutput{}, err
	}
	if err := validate(t.schema, in.Record); err != nil {
		return versionOutput{}, module.Errorf(module.CodeInvalidArgument, "%s %s: %v", l.manifest.Name, in.Entity, err)
	}
	v, err := records.Put(ctx, ns, in.Entity, in.ID, in.Record, in.version())
	return versionOutput{v}, storeError(err, in.Entity+" "+in.ID)
}

func (n *Node) storeGet(ctx context.Context, in recordRef) (store.Record, error) {
	l, ns, records, err := n.recordsOf(ctx, in.Store)
	if err != nil {
		return store.Record{}, err
	}
	if _, err := n.entity(l, ns, in.Entity); err != nil {
		return store.Record{}, err
	}
	r, err := records.Get(ctx, ns, in.Entity, in.ID)
	return r, storeError(err, in.Entity+" "+in.ID)
}

func (n *Node) storeDelete(ctx context.Context, in struct {
	recordRef
	condition
}) (struct{}, error) {
	l, ns, records, err := n.recordsOf(ctx, in.Store)
	if err != nil {
		return struct{}{}, err
	}
	if _, err := n.entity(l, ns, in.Entity); err != nil {
		return struct{}{}, err
	}
	return struct{}{}, storeError(records.Delete(ctx, ns, in.Entity, in.ID, in.version()), in.Entity+" "+in.ID)
}

func (n *Node) storeQuery(ctx context.Context, in struct {
	storeRef
	Entity string `json:"entity"`
	store.Query
}) (map[string][]store.Record, error) {
	l, ns, records, err := n.recordsOf(ctx, in.Store)
	if err != nil {
		return nil, err
	}
	t, err := n.entity(l, ns, in.Entity)
	if err != nil {
		return nil, err
	}
	indexed := func(f string) bool { return f == "id" || slices.Contains(t.indexes, f) }
	for f := range in.Where {
		if !indexed(f) {
			return nil, module.Errorf(module.CodeInvalidArgument, "%s %s: %q isn't indexed", l.manifest.Name, in.Entity, f)
		}
	}
	if in.OrderBy != "" && !indexed(in.OrderBy) {
		return nil, module.Errorf(module.CodeInvalidArgument, "%s %s: %q isn't indexed", l.manifest.Name, in.Entity, in.OrderBy)
	}
	if in.Limit == 0 {
		in.Limit = defaultLimit
	}
	found, err := records.Query(ctx, ns, in.Entity, in.Query)
	if err != nil {
		return nil, module.Errorf(module.CodeInvalidArgument, "%v", err)
	}
	return map[string][]store.Record{"records": found}, nil
}

// recordOp is one write of a batch of records, on the wire.
type recordOp struct {
	Op     string `json:"op"` // put or delete
	Entity string `json:"entity"`
	ID     string `json:"id"`
	condition
	Record json.RawMessage `json:"record"`
}

func (n *Node) storeBatch(ctx context.Context, in struct {
	storeRef
	Ops []recordOp `json:"ops"`
}) (versionsOutput, error) {
	l, ns, records, err := n.recordsOf(ctx, in.Store)
	if err != nil {
		return versionsOutput{}, err
	}
	ops := make([]store.EntityOp, len(in.Ops))
	for i, op := range in.Ops {
		t, err := n.entity(l, ns, op.Entity)
		if err != nil {
			return versionsOutput{}, fmt.Errorf("op %d: %w", i, err)
		}
		ops[i] = store.EntityOp{Entity: op.Entity, ID: op.ID, IfVersion: op.version()}
		if op.Op == "put" {
			if err := validate(t.schema, op.Record); err != nil {
				return versionsOutput{}, module.Errorf(module.CodeInvalidArgument, "op %d: %s %s: %v", i, l.manifest.Name, op.Entity, err)
			}
			ops[i].Record = op.Record
		}
	}
	versions, err := records.Batch(ctx, ns, ops)
	return versionsOutput{versions}, storeError(err, "batch")
}

type kvKey struct {
	storeRef
	Key string `json:"key"`
}

func (n *Node) kvGet(ctx context.Context, in kvKey) (store.Pair, error) {
	ns, pairs, err := n.pairsOf(ctx, in.Store)
	if err != nil {
		return store.Pair{}, err
	}
	p, err := pairs.Get(ctx, ns, in.Key)
	return p, storeError(err, "key "+in.Key)
}

func (n *Node) kvPut(ctx context.Context, in struct {
	kvKey
	condition
	Value json.RawMessage `json:"value"`
}) (versionOutput, error) {
	ns, pairs, err := n.pairsOf(ctx, in.Store)
	if err != nil {
		return versionOutput{}, err
	}
	v, err := pairs.Put(ctx, ns, in.Key, in.Value, in.version())
	return versionOutput{v}, storeError(err, "key "+in.Key)
}

func (n *Node) kvDelete(ctx context.Context, in struct {
	kvKey
	condition
}) (struct{}, error) {
	ns, pairs, err := n.pairsOf(ctx, in.Store)
	if err != nil {
		return struct{}{}, err
	}
	return struct{}{}, storeError(pairs.Delete(ctx, ns, in.Key, in.version()), "key "+in.Key)
}

func (n *Node) kvList(ctx context.Context, in struct {
	storeRef
	Prefix string `json:"prefix"`
	Limit  int    `json:"limit"`
}) (map[string][]store.Pair, error) {
	ns, pairs, err := n.pairsOf(ctx, in.Store)
	if err != nil {
		return nil, err
	}
	if in.Limit == 0 {
		in.Limit = defaultLimit
	}
	found, err := pairs.List(ctx, ns, in.Prefix, in.Limit)
	return map[string][]store.Pair{"pairs": found}, err
}

// pairOp is one write of a batch of pairs, on the wire.
type pairOp struct {
	Op  string `json:"op"` // put or delete
	Key string `json:"key"`
	condition
	Value json.RawMessage `json:"value"`
}

func (n *Node) kvBatch(ctx context.Context, in struct {
	storeRef
	Ops []pairOp `json:"ops"`
}) (versionsOutput, error) {
	ns, pairs, err := n.pairsOf(ctx, in.Store)
	if err != nil {
		return versionsOutput{}, err
	}
	ops := make([]store.KVOp, len(in.Ops))
	for i, op := range in.Ops {
		ops[i] = store.KVOp{Key: op.Key, IfVersion: op.version()}
		if op.Op == "put" {
			if op.Value == nil {
				return versionsOutput{}, module.Errorf(module.CodeInvalidArgument, "op %d: a put needs a value", i)
			}
			ops[i].Value = op.Value
		}
	}
	versions, err := pairs.Batch(ctx, ns, ops)
	return versionsOutput{versions}, storeError(err, "batch")
}
