package store_test

import (
	"context"
	"errors"
	"testing"

	"gopkg.in/yaml.v3"

	"go-decentralized/internal/store"
	_ "go-decentralized/internal/store/sqlite" // as a node has it
)

// Drivers register themselves, and a store is of the kinds its driver
// implements.
func TestOpen(t *testing.T) {
	if _, err := store.Open(store.Config{Driver: "mongo"}, ""); err == nil {
		t.Fatal("opened a driver that doesn't exist yet")
	}
	s, err := store.Open(store.Config{}, "") // sqlite, in memory
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, kind := range store.Kinds {
		if !store.Supports(s, kind) {
			t.Errorf("SQLite doesn't support %s", kind)
		}
	}
	if store.Supports(s, "blob") {
		t.Error("SQLite supports blobs")
	}
	if store.Supports(closer{}, store.KindKV) {
		t.Error("a bare store supports pairs")
	}
}

// closer is a store of no kind.
type closer struct{}

func (closer) Close() error { return nil }

// A store's block is its driver, and whatever options the driver takes.
func TestConfig(t *testing.T) {
	var cfgs map[string]store.Config
	err := yaml.Unmarshal([]byte(`
main: {driver: postgres, url: "postgres://db/app", pool: 4}
files: {path: files.db}
`), &cfgs)
	if err != nil {
		t.Fatal(err)
	}
	if c := cfgs["main"]; c.Driver != "postgres" || c.Options.String("url") != "postgres://db/app" || c.Options["pool"] != 4 {
		t.Fatalf("main = %+v", c)
	}
	if c := cfgs["files"]; c.Driver != "" || c.Options.String("path") != "files.db" || c.Options.String("url") != "" {
		t.Fatalf("files = %+v", c)
	}
}

// The node's own parts keep Go values in a store's pairs.
func TestValues(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(store.Config{}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	kv, _ := store.KV(s)
	vs := store.NewValues(kv, "routing")
	if err := vs.Put(ctx, "peers", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	var peers []string
	if err := vs.Get(ctx, "peers", &peers); err != nil || len(peers) != 2 {
		t.Fatalf("Get = %v, %v", peers, err)
	}
	if err := vs.Get(ctx, "missing", &peers); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get of a missing key: err = %v", err)
	}
}
