package module

import (
	"context"
	"encoding/json"
)

// Entities are a module's records of one of its entity types, which its node
// keeps. See spec/modules.md, "Storage".
type Entities struct {
	env    Env
	entity string
}

// Entities returns the module's records of the entity type called name.
func (env Env) Entities(name string) Entities { return Entities{env, name} }

// Query selects records by their indexed fields.
type Query struct {
	// Where maps fields to the value they must have, or to conditions:
	// {"gt": v}, {"gte": v}, {"lt": v} or {"lte": v}.
	Where   map[string]any `json:"where,omitempty"`
	OrderBy string         `json:"order_by,omitempty"`
	Desc    bool           `json:"desc,omitempty"`
	Limit   int            `json:"limit,omitempty"`
}

// Put stores record, replacing any with the same ID.
func (e Entities) Put(ctx context.Context, id string, record any) error {
	return e.env.Call(ctx, "store.put", map[string]any{"entity": e.entity, "id": id, "record": record}, nil)
}

// Get decodes the record with the given ID into out. It fails with
// CodeNotFound if there's none.
func (e Entities) Get(ctx context.Context, id string, out any) error {
	var r struct {
		Record json.RawMessage `json:"record"`
	}
	if err := e.env.Call(ctx, "store.get", map[string]any{"entity": e.entity, "id": id}, &r); err != nil {
		return err
	}
	return Decode(r.Record, out)
}

func (e Entities) Delete(ctx context.Context, id string) error {
	return e.env.Call(ctx, "store.delete", map[string]any{"entity": e.entity, "id": id}, nil)
}

// Query decodes the records that match q into out, a pointer to a slice.
func (e Entities) Query(ctx context.Context, q Query, out any) error {
	var r struct {
		Records json.RawMessage `json:"records"`
	}
	in := struct {
		Entity string `json:"entity"`
		Query
	}{e.entity, q}
	if err := e.env.Call(ctx, "store.query", in, &r); err != nil {
		return err
	}
	if len(r.Records) == 0 || string(r.Records) == "null" {
		r.Records = json.RawMessage("[]")
	}
	return Decode(r.Records, out)
}

// KV is a module's key-value pairs, which its node keeps.
type KV struct{ env Env }

// KV returns the module's key-value pairs.
func (env Env) KV() KV { return KV{env} }

// Get decodes the value of key into out. It fails with CodeNotFound if the
// key isn't there.
func (kv KV) Get(ctx context.Context, key string, out any) error {
	var r struct {
		Value json.RawMessage `json:"value"`
	}
	if err := kv.env.Call(ctx, "store.kv_get", map[string]string{"key": key}, &r); err != nil {
		return err
	}
	return Decode(r.Value, out)
}

func (kv KV) Put(ctx context.Context, key string, value any) error {
	return kv.env.Call(ctx, "store.kv_put", map[string]any{"key": key, "value": value}, nil)
}

func (kv KV) Delete(ctx context.Context, key string) error {
	return kv.env.Call(ctx, "store.kv_delete", map[string]string{"key": key}, nil)
}

// Pair is a key and its value.
type Pair struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// List returns up to limit pairs whose key starts with prefix, by key.
func (kv KV) List(ctx context.Context, prefix string, limit int) ([]Pair, error) {
	var r struct {
		Pairs []Pair `json:"pairs"`
	}
	in := map[string]any{"prefix": prefix}
	if limit > 0 {
		in["limit"] = limit
	}
	err := kv.env.Call(ctx, "store.kv_list", in, &r)
	return r.Pairs, err
}
