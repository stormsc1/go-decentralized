package node

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"go-decentralized/internal/store"
	"go-decentralized/module"
)

//go:embed store.module.yaml
var storeManifest []byte

// defaultLimit is how many records or pairs a query returns, unless it says.
const defaultLimit = 100

// entityType is one of a module's entity types, as the node stores them.
type entityType struct {
	schema  *jsonschema.Schema
	indexes []string
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

// storeOwner returns the module whose data a store call is about: the one
// making it.
func storeOwner(ctx context.Context) (string, error) {
	if m := callingModule(ctx); m != "" {
		return m, nil
	}
	return "", module.Errorf(module.CodePermissionDenied, "only modules have stores")
}

// entity returns the calling module's entity type called name.
func (n *Node) entity(ctx context.Context, name string) (string, entityType, error) {
	owner, err := storeOwner(ctx)
	if err != nil {
		return "", entityType{}, err
	}
	t, ok := n.entityType(owner, name)
	if !ok {
		return "", entityType{}, module.Errorf(module.CodeNotFound, "module %s has no entity type %q", owner, name)
	}
	return owner, t, nil
}

func (n *Node) entityType(owner, name string) (entityType, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	t, ok := n.entities[owner+"."+name]
	return t, ok
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

type recordRef struct {
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

func (n *Node) storePut(ctx context.Context, in struct {
	recordRef
	condition
	Record json.RawMessage `json:"record"`
}) (versionOutput, error) {
	owner, t, err := n.entity(ctx, in.Entity)
	if err != nil {
		return versionOutput{}, err
	}
	if err := validate(t.schema, in.Record); err != nil {
		return versionOutput{}, module.Errorf(module.CodeInvalidArgument, "%s %s: %v", owner, in.Entity, err)
	}
	v, err := n.records.Put(ctx, owner, in.Entity, in.ID, in.Record, in.version())
	return versionOutput{v}, storeError(err, in.Entity+" "+in.ID)
}

func (n *Node) storeGet(ctx context.Context, in recordRef) (store.Record, error) {
	owner, _, err := n.entity(ctx, in.Entity)
	if err != nil {
		return store.Record{}, err
	}
	r, err := n.records.Get(ctx, owner, in.Entity, in.ID)
	return r, storeError(err, in.Entity+" "+in.ID)
}

func (n *Node) storeDelete(ctx context.Context, in struct {
	recordRef
	condition
}) (struct{}, error) {
	owner, _, err := n.entity(ctx, in.Entity)
	if err != nil {
		return struct{}{}, err
	}
	return struct{}{}, storeError(n.records.Delete(ctx, owner, in.Entity, in.ID, in.version()), in.Entity+" "+in.ID)
}

func (n *Node) storeQuery(ctx context.Context, in struct {
	Entity string `json:"entity"`
	store.Query
}) (map[string][]store.Record, error) {
	owner, t, err := n.entity(ctx, in.Entity)
	if err != nil {
		return nil, err
	}
	indexed := func(f string) bool { return f == "id" || slices.Contains(t.indexes, f) }
	for f := range in.Where {
		if !indexed(f) {
			return nil, module.Errorf(module.CodeInvalidArgument, "%s %s: %q isn't indexed", owner, in.Entity, f)
		}
	}
	if in.OrderBy != "" && !indexed(in.OrderBy) {
		return nil, module.Errorf(module.CodeInvalidArgument, "%s %s: %q isn't indexed", owner, in.Entity, in.OrderBy)
	}
	if in.Limit == 0 {
		in.Limit = defaultLimit
	}
	records, err := n.records.Query(ctx, owner, in.Entity, in.Query)
	if err != nil {
		return nil, module.Errorf(module.CodeInvalidArgument, "%v", err)
	}
	return map[string][]store.Record{"records": records}, nil
}

// recordOp is one write of a batch of records, on the wire.
type recordOp struct {
	Op string `json:"op"` // put or delete
	recordRef
	condition
	Record json.RawMessage `json:"record"`
}

type versionsOutput struct {
	Versions []int64 `json:"versions"`
}

func (n *Node) storeBatch(ctx context.Context, in struct {
	Ops []recordOp `json:"ops"`
}) (versionsOutput, error) {
	owner, err := storeOwner(ctx)
	if err != nil {
		return versionsOutput{}, err
	}
	ops := make([]store.EntityOp, len(in.Ops))
	for i, op := range in.Ops {
		t, ok := n.entityType(owner, op.Entity)
		if !ok {
			return versionsOutput{}, module.Errorf(module.CodeNotFound, "op %d: module %s has no entity type %q", i, owner, op.Entity)
		}
		ops[i] = store.EntityOp{Entity: op.Entity, ID: op.ID, IfVersion: op.version()}
		if op.Op == "put" {
			if err := validate(t.schema, op.Record); err != nil {
				return versionsOutput{}, module.Errorf(module.CodeInvalidArgument, "op %d: %s %s: %v", i, owner, op.Entity, err)
			}
			ops[i].Record = op.Record
		}
	}
	versions, err := n.records.Batch(ctx, owner, ops)
	return versionsOutput{versions}, storeError(err, "batch")
}

type kvKey struct {
	Key string `json:"key"`
}

func (n *Node) kvGet(ctx context.Context, in kvKey) (store.Pair, error) {
	owner, err := storeOwner(ctx)
	if err != nil {
		return store.Pair{}, err
	}
	p, err := n.pairs.Get(ctx, owner, in.Key)
	return p, storeError(err, "key "+in.Key)
}

func (n *Node) kvPut(ctx context.Context, in struct {
	kvKey
	condition
	Value json.RawMessage `json:"value"`
}) (versionOutput, error) {
	owner, err := storeOwner(ctx)
	if err != nil {
		return versionOutput{}, err
	}
	v, err := n.pairs.Put(ctx, owner, in.Key, in.Value, in.version())
	return versionOutput{v}, storeError(err, "key "+in.Key)
}

func (n *Node) kvDelete(ctx context.Context, in struct {
	kvKey
	condition
}) (struct{}, error) {
	owner, err := storeOwner(ctx)
	if err != nil {
		return struct{}{}, err
	}
	return struct{}{}, storeError(n.pairs.Delete(ctx, owner, in.Key, in.version()), "key "+in.Key)
}

func (n *Node) kvList(ctx context.Context, in struct {
	Prefix string `json:"prefix"`
	Limit  int    `json:"limit"`
}) (map[string][]store.Pair, error) {
	owner, err := storeOwner(ctx)
	if err != nil {
		return nil, err
	}
	if in.Limit == 0 {
		in.Limit = defaultLimit
	}
	pairs, err := n.pairs.List(ctx, owner, in.Prefix, in.Limit)
	return map[string][]store.Pair{"pairs": pairs}, err
}

// pairOp is one write of a batch of pairs, on the wire.
type pairOp struct {
	Op string `json:"op"` // put or delete
	kvKey
	condition
	Value json.RawMessage `json:"value"`
}

func (n *Node) kvBatch(ctx context.Context, in struct {
	Ops []pairOp `json:"ops"`
}) (versionsOutput, error) {
	owner, err := storeOwner(ctx)
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
	versions, err := n.pairs.Batch(ctx, owner, ops)
	return versionsOutput{versions}, storeError(err, "batch")
}
