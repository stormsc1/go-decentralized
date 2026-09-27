package module

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
)

// signaturePrefix starts everything a node signs for its modules, so the
// signatures can't pass for ones the key makes elsewhere, e.g. in TLS.
const signaturePrefix = "decentralized-signature\x00"

// signed is what's signed for purpose: prefix, purpose, NUL, data.
func signed(purpose string, data []byte) ([]byte, error) {
	if purpose == "" || strings.ContainsRune(purpose, 0) {
		return nil, errors.New("invalid signature purpose")
	}
	return slices.Concat([]byte(signaturePrefix), []byte(purpose), []byte{0}, data), nil
}

// Sign signs data for purpose with key. Modules sign with Env.Sign, which
// keeps the key in the node.
func Sign(key ed25519.PrivateKey, purpose string, data []byte) ([]byte, error) {
	msg, err := signed(purpose, data)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(key, msg), nil
}

// Verify reports whether sig is pub's signature of data for purpose.
func Verify(pub ed25519.PublicKey, purpose string, data, sig []byte) bool {
	msg, err := signed(purpose, data)
	return err == nil && len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, msg, sig)
}

// NodeID is the ID of the node with the given public key: its SHA-256, in
// hex.
func NodeID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}
