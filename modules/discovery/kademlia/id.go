// Package kademlia implements a minimal Kademlia DHT used for node discovery.
package kademlia

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/bits"
	"slices"
)

// ID is a 256-bit identifier for nodes and keys in the DHT.
type ID [sha256.Size]byte

// HashKey maps an arbitrary key (e.g. a capability name) into the ID space.
func HashKey(key string) ID { return sha256.Sum256([]byte(key)) }

func ParseID(s string) (ID, error) {
	var id ID
	return id, id.UnmarshalText([]byte(s))
}

func (id ID) String() string { return hex.EncodeToString(id[:]) }

func (id ID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

func (id *ID) UnmarshalText(text []byte) error {
	b, err := hex.DecodeString(string(text))
	if err != nil || len(b) != len(id) {
		return fmt.Errorf("invalid id %q", text)
	}
	copy(id[:], b)
	return nil
}

// commonPrefixLen is the number of leading bits a and b share. It selects the
// routing table bucket: bucket i holds contacts sharing exactly i bits with us.
func commonPrefixLen(a, b ID) int {
	for i := range a {
		if x := a[i] ^ b[i]; x != 0 {
			return i*8 + bits.LeadingZeros8(x)
		}
	}
	return len(a) * 8
}

// sortByDistance orders contacts by XOR distance to target, closest first.
func sortByDistance(contacts []Contact, target ID) {
	slices.SortFunc(contacts, func(a, b Contact) int {
		da, db := a.ID.xor(target), b.ID.xor(target)
		return bytes.Compare(da[:], db[:])
	})
}

func (id ID) xor(o ID) ID {
	for i := range id {
		id[i] ^= o[i]
	}
	return id
}
