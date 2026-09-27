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
		"kv_get":    module.HandlerFor(n.kvGet),
		"kv_put":    module.HandlerFor(n.kvPut),
		"kv_delete": module.HandlerFor(n.kvDelete),
		"kv_list":   module.HandlerFor(n.kvList),
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
	n.mu.RLock()
	t, ok := n.entities[owner+"."+name]
	n.mu.RUnlock()
	if !ok {
		return "", entityType{}, module.Errorf(module.CodeNotFound, "module %s has no entity type %q", owner, name)
	}
	return owner, t, nil
}

type recordRef struct {
	Entity string `json:"entity"`
	ID     string `json:"id"`
}

func (n *Node) storePut(ctx context.Context, in struct {
	recordRef
	Record json.RawMessage `json:"record"`
}) (struct{}, error) {
	owner, t, err := n.entity(ctx, in.Entity)
	if err != nil {
		return struct{}{}, err
	}
	if err := validate(t.schema, in.Record); err != nil {
		return struct{}{}, module.Errorf(module.CodeInvalidArgument, "%s %s: %v", owner, in.Entity, err)
	}
	return struct{}{}, n.store.Put(ctx, owner, in.Entity, in.ID, in.Record)
}

func (n *Node) storeGet(ctx context.Context, in recordRef) (map[string]json.RawMessage, error) {
	owner, _, err := n.entity(ctx, in.Entity)
	if err != nil {
		return nil, err
	}
	record, err := n.store.Get(ctx, owner, in.Entity, in.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, module.Errorf(module.CodeNotFound, "no %s %q", in.Entity, in.ID)
	}
	return map[string]json.RawMessage{"record": record}, err
}

func (n *Node) storeDelete(ctx context.Context, in recordRef) (struct{}, error) {
	owner, _, err := n.entity(ctx, in.Entity)
	if err != nil {
		return struct{}{}, err
	}
	return struct{}{}, n.store.Delete(ctx, owner, in.Entity, in.ID)
}

func (n *Node) storeQuery(ctx context.Context, in struct {
	Entity string `json:"entity"`
	store.Query
}) (map[string][]json.RawMessage, error) {
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
	records, err := n.store.Query(ctx, owner, in.Entity, in.Query)
	if err != nil {
		return nil, module.Errorf(module.CodeInvalidArgument, "%v", err)
	}
	return map[string][]json.RawMessage{"records": records}, nil
}

type kvKey struct {
	Key string `json:"key"`
}

func (n *Node) kvGet(ctx context.Context, in kvKey) (map[string]json.RawMessage, error) {
	owner, err := storeOwner(ctx)
	if err != nil {
		return nil, err
	}
	value, err := n.store.KVGet(ctx, owner, in.Key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, module.Errorf(module.CodeNotFound, "no key %q", in.Key)
	}
	return map[string]json.RawMessage{"value": value}, err
}

func (n *Node) kvPut(ctx context.Context, in struct {
	kvKey
	Value json.RawMessage `json:"value"`
}) (struct{}, error) {
	owner, err := storeOwner(ctx)
	if err != nil {
		return struct{}{}, err
	}
	return struct{}{}, n.store.KVPut(ctx, owner, in.Key, in.Value)
}

func (n *Node) kvDelete(ctx context.Context, in kvKey) (struct{}, error) {
	owner, err := storeOwner(ctx)
	if err != nil {
		return struct{}{}, err
	}
	return struct{}{}, n.store.KVDelete(ctx, owner, in.Key)
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
	pairs, err := n.store.KVList(ctx, owner, in.Prefix, in.Limit)
	return map[string][]store.Pair{"pairs": pairs}, err
}
