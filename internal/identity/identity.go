// Package identity manages a node's key pair, from which its ID is derived.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Load reads the node key stored at path, creating it, and its directory, if
// it does not exist. An empty path returns a fresh key that is not saved.
func Load(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		return key, err
	}
	seed, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		return key, os.WriteFile(path, key.Seed(), 0o600)
	}
	if err != nil {
		return nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("invalid key file " + path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// NodeID derives a node's ID from its key: hex(sha256(public key)).
func NodeID(key ed25519.PrivateKey) string {
	return IDOf(key.Public().(ed25519.PublicKey))
}

// IDOf is the ID of the node with the given public key.
func IDOf(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}
