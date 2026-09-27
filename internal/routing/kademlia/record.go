package kademlia

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"time"

	"go-decentralized/module"
)

// recordPurpose is what records are signed for, see module.Verify.
const recordPurpose = "routing.record"

// Record is a node's self-signed description: its key, name and every
// address it can be reached at, including indirect ones such as relays. Only
// the key's owner can create it, so any node can store and pass records
// along without being trusted. The signature covers Data as is, so nodes
// never need to re-encode a record to check it.
type Record struct {
	// Data is a recordData, encoded.
	Data []byte `json:"data"`
	Sig  []byte `json:"sig"`
	d    recordData
}

type recordData struct {
	PublicKey ed25519.PublicKey `json:"public_key"`
	Name      string            `json:"name,omitempty"`
	Addrs     []string          `json:"addrs"`
	// Time is when the record was signed, in Unix milliseconds. Newer
	// records replace older ones, and records expire.
	Time int64 `json:"time"`
}

func newRecord(ctx context.Context, sign SignFunc, pub ed25519.PublicKey, name string, addrs []string) (Record, error) {
	r := Record{d: recordData{PublicKey: pub, Name: name, Addrs: addrs, Time: time.Now().UnixMilli()}}
	data, err := json.Marshal(r.d)
	if err != nil {
		return r, err
	}
	sig, err := sign(ctx, recordPurpose, data)
	r.Data, r.Sig = data, sig
	return r, err
}

// verified returns r, parsed, and whether it is fresh and signed by its key.
func (r Record) verified(ttl time.Duration) (Record, bool) {
	if err := json.Unmarshal(r.Data, &r.d); err != nil {
		return r, false
	}
	return r, r.fresh(ttl) && module.Verify(r.d.PublicKey, recordPurpose, r.Data, r.Sig)
}

// ID is the ID of the node the record describes.
func (r Record) ID() ID { return keyID(r.d.PublicKey) }

func (r Record) Contact() Contact { return Contact{ID: r.ID(), Addrs: r.d.Addrs, Name: r.d.Name} }

// fresh reports whether r was signed less than ttl ago, allowing as much
// clock skew.
func (r Record) fresh(ttl time.Duration) bool {
	age := time.Since(time.UnixMilli(r.d.Time))
	return -ttl < age && age < ttl
}

// keyID derives a node's ID from its public key.
func keyID(pub ed25519.PublicKey) ID { return sha256.Sum256(pub) }

// keepNewest stores r in m unless m holds a newer record of the same node.
func keepNewest(m map[ID]Record, r Record) {
	if old, ok := m[r.ID()]; !ok || old.d.Time < r.d.Time {
		m[r.ID()] = r
	}
}
