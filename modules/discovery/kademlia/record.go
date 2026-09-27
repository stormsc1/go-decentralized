package kademlia

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"time"
)

// Record is a node's self-signed description: its key, name and every
// address it can be reached at, including indirect ones such as relays. Only
// the key's owner can create it, so any node can store and pass records
// along without being trusted.
type Record struct {
	PublicKey ed25519.PublicKey `json:"public_key"`
	Name      string            `json:"name,omitempty"`
	Addrs     []string          `json:"addrs"`
	// Time is when the record was signed, in unix nanoseconds. Newer records
	// replace older ones, and records expire.
	Time int64  `json:"time"`
	Sig  []byte `json:"sig"`
}

func newRecord(key ed25519.PrivateKey, name string, addrs []string) Record {
	r := Record{PublicKey: key.Public().(ed25519.PublicKey), Name: name, Addrs: addrs, Time: time.Now().UnixNano()}
	r.Sig = ed25519.Sign(key, r.signedBytes())
	return r
}

// ID is the ID of the node the record describes.
func (r Record) ID() ID { return keyID(r.PublicKey) }

func (r Record) Contact() Contact { return Contact{ID: r.ID(), Addrs: r.Addrs, Name: r.Name} }

// valid reports whether r is fresh and signed by its key.
func (r Record) valid(ttl time.Duration) bool {
	return len(r.PublicKey) == ed25519.PublicKeySize && r.fresh(ttl) &&
		ed25519.Verify(r.PublicKey, r.signedBytes(), r.Sig)
}

// fresh reports whether r was signed less than ttl ago, allowing as much
// clock skew.
func (r Record) fresh(ttl time.Duration) bool {
	age := time.Since(time.Unix(0, r.Time))
	return -ttl < age && age < ttl
}

// signedBytes is what the signature covers: the record without it.
func (r Record) signedBytes() []byte {
	r.Sig = nil
	b, _ := json.Marshal(r)
	return b
}

// keyID derives a node's ID from its public key.
func keyID(pub ed25519.PublicKey) ID { return sha256.Sum256(pub) }

// keepNewest stores r in m unless m holds a newer record of the same node.
func keepNewest(m map[ID]Record, r Record) {
	if old, ok := m[r.ID()]; !ok || old.Time < r.Time {
		m[r.ID()] = r
	}
}
