package module

import (
	"context"
	"encoding/json"
	"time"
)

// CodeConflict fails a write that required a version the record or pair
// doesn't have: someone else wrote first.
const CodeConflict = "store.conflict"

// MaxBlob is the most bytes one blob call carries, until there are streams.
const MaxBlob = 512 << 10

// Record is a record with its ID and version, which counts its writes from
// 1.
type Record struct {
	ID      string          `json:"id"`
	Version int64           `json:"version"`
	Data    json.RawMessage `json:"record"`
}

// Decode decodes the record into v.
func (r Record) Decode(v any) error { return Decode(r.Data, v) }

// Pair is a key and its value, with the value's version.
type Pair struct {
	Key     string          `json:"key"`
	Version int64           `json:"version"`
	Value   json.RawMessage `json:"value"`
}

// Decode decodes the value into v.
func (p Pair) Decode(v any) error { return Decode(p.Value, v) }

// A Store is one of the module's stores, which its manifest declares and
// its node keeps. See spec/modules.md, "Storage".
type Store struct {
	env  Env
	name string
}

// Store returns the module's store called name, as its manifest names it.
// An empty name means the module's only store of the kind then used.
func (env Env) Store(name string) Store { return Store{env, name} }

// Entities returns the module's records of the entity type called name, in
// its only entity store.
func (env Env) Entities(name string) Entities { return env.Store("").Entities(name) }

// KV returns the module's key-value pairs, in its only key-value store.
func (env Env) KV() KV { return env.Store("").KV() }

// Batch starts a batch of writes to the records in the module's only entity
// store.
func (env Env) Batch() *Batch { return env.Store("").Batch() }

// Blobs returns the module's blobs, in its only blob store.
func (env Env) Blobs() Blobs { return env.Store("").Blobs() }

// call calls a store capability about this store.
func (s Store) call(ctx context.Context, name string, in map[string]any, out any) error {
	if s.name != "" {
		in["store"] = s.name
	}
	return s.env.Call(ctx, "store."+name, in, out)
}

// Entities are the records of one of a module's entity types.
type Entities struct {
	store  Store
	entity string
}

// Entities returns the store's records of the entity type called name.
func (s Store) Entities(name string) Entities { return Entities{s, name} }

// Query selects records by their indexed fields.
type Query struct {
	// Where maps fields to the value they must have, or to conditions:
	// {"gt": v}, {"gte": v}, {"lt": v} or {"lte": v}.
	Where   map[string]any `json:"where,omitempty"`
	OrderBy string         `json:"order_by,omitempty"`
	Desc    bool           `json:"desc,omitempty"`
	Limit   int            `json:"limit,omitempty"`
}

// Put stores record, replacing any with the same ID, and returns its new
// version.
func (e Entities) Put(ctx context.Context, id string, record any) (int64, error) {
	return e.put(ctx, id, record, nil)
}

// PutIf stores record only if the record with that ID has the given version,
// 0 if there is none yet. Otherwise it fails with CodeConflict.
func (e Entities) PutIf(ctx context.Context, id string, record any, version int64) (int64, error) {
	return e.put(ctx, id, record, &version)
}

func (e Entities) put(ctx context.Context, id string, record any, ifVersion *int64) (int64, error) {
	var out struct{ Version int64 }
	in := map[string]any{"entity": e.entity, "id": id, "record": record}
	if ifVersion != nil {
		in["if_version"] = *ifVersion
	}
	return out.Version, e.store.call(ctx, "put", in, &out)
}

// Get decodes the record with the given ID into out and returns its version.
// It fails with CodeNotFound if there's none.
func (e Entities) Get(ctx context.Context, id string, out any) (int64, error) {
	var r Record
	if err := e.store.call(ctx, "get", map[string]any{"entity": e.entity, "id": id}, &r); err != nil {
		return 0, err
	}
	return r.Version, r.Decode(out)
}

func (e Entities) Delete(ctx context.Context, id string) error {
	return e.store.call(ctx, "delete", map[string]any{"entity": e.entity, "id": id}, nil)
}

// DeleteIf deletes the record only if it has the given version, else fails
// with CodeConflict.
func (e Entities) DeleteIf(ctx context.Context, id string, version int64) error {
	return e.store.call(ctx, "delete", map[string]any{"entity": e.entity, "id": id, "if_version": version}, nil)
}

// Query returns the records that match q.
func (e Entities) Query(ctx context.Context, q Query) ([]Record, error) {
	var out struct{ Records []Record }
	in := map[string]any{"entity": e.entity, "order_by": q.OrderBy, "desc": q.Desc}
	if q.Where != nil {
		in["where"] = q.Where
	}
	if q.Limit > 0 {
		in["limit"] = q.Limit
	}
	return out.Records, e.store.call(ctx, "query", in, &out)
}

// A Batch is writes to a store's records, of any of its entity types, that
// happen all or none. See Store.Batch.
type Batch struct {
	store Store
	ops   []map[string]any
}

// Batch starts a batch of writes to the store's records. Commit applies
// them.
func (s Store) Batch() *Batch { return &Batch{store: s} }

func (b *Batch) add(op map[string]any, ifVersion *int64) *Batch {
	if ifVersion != nil {
		op["if_version"] = *ifVersion
	}
	b.ops = append(b.ops, op)
	return b
}

func (b *Batch) Put(entity, id string, record any) *Batch {
	return b.add(map[string]any{"op": "put", "entity": entity, "id": id, "record": record}, nil)
}

func (b *Batch) PutIf(entity, id string, record any, version int64) *Batch {
	return b.add(map[string]any{"op": "put", "entity": entity, "id": id, "record": record}, &version)
}

func (b *Batch) Delete(entity, id string) *Batch {
	return b.add(map[string]any{"op": "delete", "entity": entity, "id": id}, nil)
}

func (b *Batch) DeleteIf(entity, id string, version int64) *Batch {
	return b.add(map[string]any{"op": "delete", "entity": entity, "id": id}, &version)
}

// Commit applies the batch's writes in order, all or none, and returns the
// new version of each put, 0 for deletes. A conditional write that fails
// fails the whole batch with CodeConflict.
func (b *Batch) Commit(ctx context.Context) ([]int64, error) {
	var out struct{ Versions []int64 }
	return out.Versions, b.store.call(ctx, "batch", map[string]any{"ops": b.ops}, &out)
}

// KV is the key-value pairs of one of a module's stores.
type KV struct{ store Store }

// KV returns the store's key-value pairs.
func (s Store) KV() KV { return KV{s} }

// Get decodes the value of key into out and returns its version. It fails
// with CodeNotFound if the key isn't there.
func (kv KV) Get(ctx context.Context, key string, out any) (int64, error) {
	var p Pair
	if err := kv.store.call(ctx, "kv_get", map[string]any{"key": key}, &p); err != nil {
		return 0, err
	}
	return p.Version, p.Decode(out)
}

// Put sets the value of key and returns its new version.
func (kv KV) Put(ctx context.Context, key string, value any) (int64, error) {
	var out struct{ Version int64 }
	return out.Version, kv.store.call(ctx, "kv_put", map[string]any{"key": key, "value": value}, &out)
}

// PutIf sets the value only if it has the given version, 0 if the key isn't
// there yet. Otherwise it fails with CodeConflict.
func (kv KV) PutIf(ctx context.Context, key string, value any, version int64) (int64, error) {
	var out struct{ Version int64 }
	return out.Version, kv.store.call(ctx, "kv_put", map[string]any{"key": key, "value": value, "if_version": version}, &out)
}

func (kv KV) Delete(ctx context.Context, key string) error {
	return kv.store.call(ctx, "kv_delete", map[string]any{"key": key}, nil)
}

// DeleteIf deletes the key only if its value has the given version, else
// fails with CodeConflict.
func (kv KV) DeleteIf(ctx context.Context, key string, version int64) error {
	return kv.store.call(ctx, "kv_delete", map[string]any{"key": key, "if_version": version}, nil)
}

// List returns up to limit pairs whose key starts with prefix, by key.
func (kv KV) List(ctx context.Context, prefix string, limit int) ([]Pair, error) {
	var out struct{ Pairs []Pair }
	in := map[string]any{"prefix": prefix}
	if limit > 0 {
		in["limit"] = limit
	}
	return out.Pairs, kv.store.call(ctx, "kv_list", in, &out)
}

// A KVBatch is writes to a store's pairs that happen all or none. See
// KV.Batch.
type KVBatch struct {
	store Store
	ops   []map[string]any
}

// Batch starts a batch of writes to the pairs. Commit applies them.
func (kv KV) Batch() *KVBatch { return &KVBatch{store: kv.store} }

func (b *KVBatch) add(op map[string]any, ifVersion *int64) *KVBatch {
	if ifVersion != nil {
		op["if_version"] = *ifVersion
	}
	b.ops = append(b.ops, op)
	return b
}

func (b *KVBatch) Put(key string, value any) *KVBatch {
	return b.add(map[string]any{"op": "put", "key": key, "value": value}, nil)
}

func (b *KVBatch) PutIf(key string, value any, version int64) *KVBatch {
	return b.add(map[string]any{"op": "put", "key": key, "value": value}, &version)
}

func (b *KVBatch) Delete(key string) *KVBatch {
	return b.add(map[string]any{"op": "delete", "key": key}, nil)
}

func (b *KVBatch) DeleteIf(key string, version int64) *KVBatch {
	return b.add(map[string]any{"op": "delete", "key": key}, &version)
}

// Commit applies the batch's writes in order, all or none, and returns the
// new version of each put, 0 for deletes. A conditional write that fails
// fails the whole batch with CodeConflict.
func (b *KVBatch) Commit(ctx context.Context) ([]int64, error) {
	var out struct{ Versions []int64 }
	return out.Versions, b.store.call(ctx, "kv_batch", map[string]any{"ops": b.ops}, &out)
}

// BlobInfo describes a blob: bytes under a key, with a content type. Blobs
// have no versions: the last write wins.
type BlobInfo struct {
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type,omitempty"`
	Modified    time.Time `json:"modified"`
}

// Blobs are the blobs of one of a module's stores.
type Blobs struct{ store Store }

// Blobs returns the store's blobs.
func (s Store) Blobs() Blobs { return Blobs{s} }

// Put writes data under key, replacing what was there: up to MaxBlob bytes.
func (b Blobs) Put(ctx context.Context, key string, data []byte, contentType string) error {
	return b.store.call(ctx, "blob_put", map[string]any{"key": key, "data": data, "content_type": contentType}, nil)
}

// Get returns the blob's bytes and description. It fails with CodeNotFound
// if the key isn't there.
func (b Blobs) Get(ctx context.Context, key string) ([]byte, BlobInfo, error) {
	var out struct {
		BlobInfo
		Data []byte `json:"data"`
	}
	err := b.store.call(ctx, "blob_get", map[string]any{"key": key}, &out)
	return out.Data, out.BlobInfo, err
}

// Stat describes the blob without reading it.
func (b Blobs) Stat(ctx context.Context, key string) (BlobInfo, error) {
	var out BlobInfo
	return out, b.store.call(ctx, "blob_stat", map[string]any{"key": key}, &out)
}

func (b Blobs) Delete(ctx context.Context, key string) error {
	return b.store.call(ctx, "blob_delete", map[string]any{"key": key}, nil)
}

// List describes up to limit blobs whose key starts with prefix, by key.
func (b Blobs) List(ctx context.Context, prefix string, limit int) ([]BlobInfo, error) {
	var out struct{ Blobs []BlobInfo }
	in := map[string]any{"prefix": prefix}
	if limit > 0 {
		in["limit"] = limit
	}
	return out.Blobs, b.store.call(ctx, "blob_list", in, &out)
}
