package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// open opens a SQLite store at path, in memory if it's empty.
func open(t *testing.T, path string) Store {
	t.Helper()
	var dataDir string
	if path != "" {
		dataDir = filepath.Dir(path)
	}
	s, err := Open(Config{Driver: "sqlite", Path: path}, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// kinds returns the store's records and pairs, which SQLite both keeps.
func kinds(t *testing.T, s Store) (EntityStore, KVStore) {
	t.Helper()
	es, ok := Entities(s)
	if !ok {
		t.Fatal("SQLite doesn't keep records")
	}
	kv, ok := KV(s)
	if !ok {
		t.Fatal("SQLite doesn't keep pairs")
	}
	return es, kv
}

func ids(records []Record) string {
	var out []string
	for _, r := range records {
		out = append(out, r.ID)
	}
	return strings.Join(out, ",")
}

func TestRecords(t *testing.T) {
	ctx := context.Background()
	s, _ := kinds(t, open(t, ""))
	if err := s.Index(ctx, "chat", "event", "channel"); err != nil {
		t.Fatal(err)
	}
	if err := s.Index(ctx, "chat", "event", "time"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{
		`{"id":"a","channel":"x","time":3}`,
		`{"id":"b","channel":"x","time":1}`,
		`{"id":"c","channel":"y","time":2}`,
		`{"id":"d","channel":"x","time":2,"pinned":true}`,
	} {
		var v struct{ ID string }
		_ = json.Unmarshal([]byte(r), &v)
		if _, err := s.Put(ctx, "chat", "event", v.ID, json.RawMessage(r), Any); err != nil {
			t.Fatal(err)
		}
	}
	// Another namespace's records of the same entity type are its own.
	if _, err := s.Put(ctx, "other", "event", "a", json.RawMessage(`{"id":"a","channel":"x","time":9}`), Any); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		q    Query
		want string
	}{
		{Query{Limit: 10}, "a,b,c,d"},
		{Query{Where: map[string]any{"channel": "x"}, OrderBy: "time", Limit: 10}, "b,d,a"},
		{Query{Where: map[string]any{"channel": "x"}, OrderBy: "time", Desc: true, Limit: 2}, "a,d"},
		{Query{Where: map[string]any{"time": map[string]any{"gte": 2.0, "lt": 3.0}}, Limit: 10}, "c,d"},
		{Query{Where: map[string]any{"pinned": true}, Limit: 10}, "d"},
	} {
		records, err := s.Query(ctx, "chat", "event", tc.q)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(records); got != tc.want {
			t.Errorf("Query(%+v) = %s, want %s", tc.q, got, tc.want)
		}
	}

	if err := s.Delete(ctx, "chat", "event", "a", Any); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "chat", "event", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v", err)
	}
	if r, err := s.Get(ctx, "other", "event", "a"); err != nil || !strings.Contains(string(r.Data), `"time":9`) || r.Version != 1 {
		t.Fatalf("the other namespace's record = %+v, %v", r, err)
	}
	if _, err := s.Query(ctx, "chat", "event", Query{Where: map[string]any{"time; DROP TABLE": 1}}); err == nil {
		t.Fatal("queried by a field that isn't a plain name")
	}
}

// Every write bumps a version, and a write can require the version it
// replaces, so several writers don't overwrite each other unawares.
func TestVersions(t *testing.T) {
	ctx := context.Background()
	s, kv := kinds(t, open(t, ""))
	put := func(ifVersion int64) (int64, error) {
		return s.Put(ctx, "chat", "channel", "c", json.RawMessage(`{"name":"general"}`), ifVersion)
	}
	if v, err := put(0); err != nil || v != 1 { // only if new
		t.Fatalf("first put = %d, %v", v, err)
	}
	if _, err := put(0); !errors.Is(err, ErrConflict) {
		t.Fatalf("put of an existing record as new: err = %v", err)
	}
	if v, err := put(1); err != nil || v != 2 {
		t.Fatalf("put on version 1 = %d, %v", v, err)
	}
	var c *Conflict
	if _, err := put(1); !errors.As(err, &c) || c.Want != 1 || c.Have != 2 {
		t.Fatalf("stale put: err = %v", err)
	}
	if v, err := put(Any); err != nil || v != 3 {
		t.Fatalf("unconditional put = %d, %v", v, err)
	}
	if r, err := s.Get(ctx, "chat", "channel", "c"); err != nil || r.Version != 3 {
		t.Fatalf("Get = %+v, %v", r, err)
	}
	if err := s.Delete(ctx, "chat", "channel", "c", 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete: err = %v", err)
	}
	if err := s.Delete(ctx, "chat", "channel", "c", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := put(1); !errors.As(err, &c) || c.Have != 0 {
		t.Fatalf("put on a deleted record: err = %v", err)
	}

	// Pairs the same.
	if v, err := kv.Put(ctx, "chat", "k", json.RawMessage(`1`), 0); err != nil || v != 1 {
		t.Fatalf("kv.Put = %d, %v", v, err)
	}
	if _, err := kv.Put(ctx, "chat", "k", json.RawMessage(`2`), 5); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale kv.Put: err = %v", err)
	}
	if p, err := kv.Get(ctx, "chat", "k"); err != nil || p.Version != 1 || string(p.Value) != "1" {
		t.Fatalf("kv.Get = %+v, %v", p, err)
	}
	if err := kv.Delete(ctx, "chat", "k", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Get(ctx, "chat", "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("kv.Get after delete: err = %v", err)
	}
}

// A batch happens entirely or not at all.
func TestBatches(t *testing.T) {
	ctx := context.Background()
	s, kv := kinds(t, open(t, ""))
	versions, err := s.Batch(ctx, "chat", []EntityOp{
		{Entity: "event", ID: "e1", Record: json.RawMessage(`{"text":"hi"}`), IfVersion: 0},
		{Entity: "channel", ID: "c", Record: json.RawMessage(`{"heads":["e1"]}`), IfVersion: Any},
	})
	if err != nil || len(versions) != 2 || versions[0] != 1 || versions[1] != 1 {
		t.Fatalf("Batch = %v, %v", versions, err)
	}
	// The second op fails, so the first doesn't happen either.
	_, err = s.Batch(ctx, "chat", []EntityOp{
		{Entity: "event", ID: "e2", Record: json.RawMessage(`{"text":"again"}`), IfVersion: Any},
		{Entity: "channel", ID: "c", Record: json.RawMessage(`{"heads":["e2"]}`), IfVersion: 7},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Batch with a stale op: err = %v", err)
	}
	if _, err := s.Get(ctx, "chat", "event", "e2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the failed batch's first op happened: err = %v", err)
	}
	if r, err := s.Get(ctx, "chat", "channel", "c"); err != nil || string(r.Data) != `{"heads":["e1"]}` {
		t.Fatalf("channel = %+v, %v", r, err)
	}
	versions, err = s.Batch(ctx, "chat", []EntityOp{
		{Entity: "event", ID: "e1", IfVersion: 1}, // a delete
		{Entity: "channel", ID: "c", IfVersion: Any},
	})
	if err != nil || versions[0] != 0 {
		t.Fatalf("Batch of deletes = %v, %v", versions, err)
	}

	// Pairs the same.
	versions, err = kv.Batch(ctx, "chat", []KVOp{
		{Key: "a", Value: json.RawMessage(`1`), IfVersion: 0},
		{Key: "b", Value: json.RawMessage(`2`), IfVersion: 0},
	})
	if err != nil || len(versions) != 2 || versions[1] != 1 {
		t.Fatalf("kv.Batch = %v, %v", versions, err)
	}
	_, err = kv.Batch(ctx, "chat", []KVOp{
		{Key: "a", IfVersion: Any}, // a delete
		{Key: "b", Value: json.RawMessage(`3`), IfVersion: 9},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("kv.Batch with a stale op: err = %v", err)
	}
	if _, err := kv.Get(ctx, "chat", "a"); err != nil {
		t.Fatalf("the failed batch's delete happened: err = %v", err)
	}
}

func TestKeyValues(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "node.db")
	_, kv := kinds(t, open(t, path))
	for _, k := range []string{"seen/b", "seen/a", "seen", "settings"} {
		if _, err := kv.Put(ctx, "chat", k, json.RawMessage(`"`+k+`"`), Any); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := kv.Put(ctx, "chat", "seen/a", json.RawMessage(`"again"`), Any); err != nil {
		t.Fatal(err)
	}
	pairs, err := kv.List(ctx, "chat", "seen/", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0].Key != "seen/a" || string(pairs[0].Value) != `"again"` || pairs[0].Version != 2 || pairs[1].Key != "seen/b" {
		t.Fatalf("pairs = %+v", pairs)
	}

	// What's written survives reopening the database.
	_, kv = kinds(t, open(t, path))
	if p, err := kv.Get(ctx, "chat", "settings"); err != nil || string(p.Value) != `"settings"` {
		t.Fatalf("Get after reopening = %+v, %v", p, err)
	}
	if _, err := kv.Get(ctx, "other", "settings"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another namespace read chat's key: err = %v", err)
	}

	// The node's own parts keep Go values.
	vs := NewValues(kv, "routing")
	if err := vs.Put(ctx, "peers", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	var peers []string
	if err := vs.Get(ctx, "peers", &peers); err != nil || len(peers) != 2 {
		t.Fatalf("Get = %v, %v", peers, err)
	}
	if err := vs.Get(ctx, "missing", &peers); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of a missing key: err = %v", err)
	}
}

func TestOpen(t *testing.T) {
	if _, err := Open(Config{Driver: "postgres"}, ""); err == nil {
		t.Fatal("opened a driver that doesn't exist yet")
	}
	s := open(t, "")
	for _, kind := range Kinds {
		if !Supports(s, kind) {
			t.Errorf("SQLite doesn't support %s", kind)
		}
	}
	if Supports(s, "blob") {
		t.Error("SQLite supports blobs")
	}
	// Without a data directory, everything is in memory, whatever the path.
	if got := sqlitePath("node.db", ""); got != "" {
		t.Fatalf("sqlitePath without a data dir = %q", got)
	}
	if got := sqlitePath(":memory:", "/data"); got != "" {
		t.Fatalf("sqlitePath(:memory:) = %q", got)
	}
	if got := sqlitePath("node.db", "/data"); got != filepath.Join("/data", "node.db") {
		t.Fatalf("sqlitePath = %q", got)
	}
}
