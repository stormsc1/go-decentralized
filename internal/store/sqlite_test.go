package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T, path string) *SQLite {
	t.Helper()
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ids(t *testing.T, records []json.RawMessage) string {
	t.Helper()
	var out []string
	for _, r := range records {
		var v struct{ ID string }
		if err := json.Unmarshal(r, &v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v.ID)
	}
	return strings.Join(out, ",")
}

func TestRecords(t *testing.T) {
	ctx := context.Background()
	s := open(t, "")
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
		if err := s.Put(ctx, "chat", "event", v.ID, json.RawMessage(r)); err != nil {
			t.Fatal(err)
		}
	}
	// Another module's records of the same entity type are its own.
	if err := s.Put(ctx, "other", "event", "a", json.RawMessage(`{"id":"a","channel":"x","time":9}`)); err != nil {
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
		if got := ids(t, records); got != tc.want {
			t.Errorf("Query(%+v) = %s, want %s", tc.q, got, tc.want)
		}
	}

	if err := s.Delete(ctx, "chat", "event", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "chat", "event", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v", err)
	}
	if r, err := s.Get(ctx, "other", "event", "a"); err != nil || !strings.Contains(string(r), `"time":9`) {
		t.Fatalf("the other module's record = %s, %v", r, err)
	}
	if _, err := s.Query(ctx, "chat", "event", Query{Where: map[string]any{"time; DROP TABLE": 1}}); err == nil {
		t.Fatal("queried by a field that isn't a plain name")
	}
}

func TestKeyValues(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "node.db")
	s := open(t, path)
	for _, k := range []string{"seen/b", "seen/a", "seen", "settings"} {
		if err := s.KVPut(ctx, "chat", k, json.RawMessage(`"`+k+`"`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.KVPut(ctx, "chat", "seen/a", json.RawMessage(`"again"`)); err != nil {
		t.Fatal(err)
	}
	pairs, err := s.KVList(ctx, "chat", "seen/", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0].Key != "seen/a" || string(pairs[0].Value) != `"again"` || pairs[1].Key != "seen/b" {
		t.Fatalf("pairs = %+v", pairs)
	}
	s.Close()

	// What's written survives reopening the database.
	s = open(t, path)
	if v, err := s.KVGet(ctx, "chat", "settings"); err != nil || string(v) != `"settings"` {
		t.Fatalf("KVGet after reopening = %s, %v", v, err)
	}
	if _, err := s.KVGet(ctx, "other", "settings"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another module read chat's key: err = %v", err)
	}
}
