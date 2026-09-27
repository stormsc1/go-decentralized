// Package did identifies people, organisations and other entities with
// decentralized identifiers (W3C DIDs), and carries data they sign. Only
// did:key with Ed25519 keys is supported. See spec/identity.md.
package did

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"slices"
	"strings"
)

// prefix starts every did:key, followed by its key in base58btc.
const prefix = "did:key:z"

// ed25519Codec marks an Ed25519 public key: the multicodec ed25519-pub, as a
// varint.
var ed25519Codec = []byte{0xed, 0x01}

// Key returns the did:key of an Ed25519 public key.
func Key(pub ed25519.PublicKey) string {
	return prefix + base58(slices.Concat(ed25519Codec, pub))
}

// PublicKey returns the Ed25519 public key a did:key names.
func PublicKey(did string) (ed25519.PublicKey, error) {
	enc, ok := strings.CutPrefix(did, prefix)
	// Ed25519 keys take 48 digits; decoding longer strings is quadratic.
	if !ok || len(enc) > 64 {
		return nil, fmt.Errorf("%.80q isn't an Ed25519 did:key", did)
	}
	b, err := unbase58(enc)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", did, err)
	}
	key, ok := bytes.CutPrefix(b, ed25519Codec)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%q isn't an Ed25519 did:key", did)
	}
	return ed25519.PublicKey(key), nil
}

// New makes a key pair, and returns its did:key and private key.
func New() (string, ed25519.PrivateKey, error) {
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", nil, err
	}
	return Key(pub), key, nil
}

// alphabet is base58btc's, Bitcoin's.
const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// base58 encodes b in base58btc: leading zero bytes as 1s, the rest as a
// number in base 58.
func base58(b []byte) string {
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}
	var digits []byte // base 58, least significant first
	for _, c := range b[zeros:] {
		carry := int(c)
		for i := range digits {
			carry += int(digits[i]) << 8
			digits[i] = byte(carry % 58)
			carry /= 58
		}
		for ; carry > 0; carry /= 58 {
			digits = append(digits, byte(carry%58))
		}
	}
	out := make([]byte, zeros+len(digits))
	for i := range zeros {
		out[i] = alphabet[0]
	}
	for i, d := range digits {
		out[len(out)-1-i] = alphabet[d]
	}
	return string(out)
}

// unbase58 decodes base58btc.
func unbase58(s string) ([]byte, error) {
	zeros := 0
	for zeros < len(s) && s[zeros] == alphabet[0] {
		zeros++
	}
	var num []byte // base 256, least significant first
	for i := zeros; i < len(s); i++ {
		carry := strings.IndexByte(alphabet, s[i])
		if carry < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", s[i])
		}
		for j := range num {
			carry += int(num[j]) * 58
			num[j] = byte(carry)
			carry >>= 8
		}
		for ; carry > 0; carry >>= 8 {
			num = append(num, byte(carry))
		}
	}
	out := make([]byte, zeros+len(num))
	for i, b := range num {
		out[len(out)-1-i] = b
	}
	return out, nil
}
