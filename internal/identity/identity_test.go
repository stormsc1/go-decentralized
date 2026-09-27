package identity

import (
	"crypto/ed25519"
	"path/filepath"
	"testing"
)

func TestLoadKeepsKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "node.key")
	created, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Equal(loaded) {
		t.Fatal("loaded a different key than was created")
	}
}

func TestLoadWithoutPathIsEphemeral(t *testing.T) {
	a, _ := Load("")
	b, _ := Load("")
	if a.Equal(b) {
		t.Fatal("got the same key twice")
	}
}

func TestNodeID(t *testing.T) {
	key, _ := Load("")
	id := NodeID(key)
	if len(id) != 64 || id != IDOf(key.Public().(ed25519.PublicKey)) {
		t.Fatalf("NodeID = %q", id)
	}
}
