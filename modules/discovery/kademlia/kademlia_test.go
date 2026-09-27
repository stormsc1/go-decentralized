package kademlia

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"go-decentralized/internal/api"
	"go-decentralized/internal/module"
)

func TestTableKeepsLongLivedContacts(t *testing.T) {
	self := ID{}
	table := NewTable(self)
	// IDs starting with a 1 bit share no prefix with self: all one bucket.
	for i := range K + 5 {
		table.Add(Contact{ID: ID{0x80, byte(i)}, Addrs: []string{fmt.Sprint(i)}})
	}
	if n := table.Len(); n != K {
		t.Fatalf("bucket holds %d contacts, want %d", n, K)
	}
	first := ID{0x80, 0}
	if !slices.ContainsFunc(table.All(), func(c Contact) bool { return c.ID == first }) {
		t.Fatal("full bucket dropped its longest-known contact")
	}
	table.Remove(first)
	table.Add(Contact{ID: self, Addrs: []string{"self"}})
	table.Add(Contact{ID: ID{0x40}}) // no address: a client-mode node
	if n := table.Len(); n != K-1 {
		t.Fatalf("table holds %d contacts, want %d", n, K-1)
	}
}

func TestClosestOrdersByXORDistance(t *testing.T) {
	table := NewTable(ID{})
	for _, b := range []byte{0x01, 0x80, 0x10, 0x03} {
		table.Add(Contact{ID: ID{b}, Addrs: []string{"addr"}})
	}
	var got []byte
	for _, c := range table.Closest(ID{0x02}, 3) {
		got = append(got, c.ID[0])
	}
	// Distances to 0x02: 0x03 → 1, 0x01 → 3, 0x10 → 0x12, 0x80 → 0x82.
	if want := []byte{0x03, 0x01, 0x10}; !slices.Equal(got, want) {
		t.Fatalf("closest = %x, want %x", got, want)
	}
}

func TestRecordSignature(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	r := newRecord(key, "node", []string{"203.0.113.1:443"})
	if !r.valid(time.Minute) {
		t.Fatal("fresh record is invalid")
	}
	forged := r
	forged.Addrs = []string{"203.0.113.66:443"}
	if forged.valid(time.Minute) {
		t.Fatal("altered record is valid")
	}
	old := r
	old.Time = time.Now().Add(-time.Hour).UnixNano()
	old.Sig = ed25519.Sign(key, old.signedBytes())
	if old.valid(time.Minute) {
		t.Fatal("expired record is valid")
	}
}

// memNet routes DHT messages between nodes in memory, by address, refusing
// to reach a node other than the one expected, as TLS does.
type memNet map[string]*DHT

func (m memNet) sender(from ID) module.SendFunc {
	return func(ctx context.Context, to api.Peer, name string, req, resp any) error {
		for _, addr := range to.Addrs {
			d := m[addr]
			if d == nil || (to.ID != "" && to.ID != d.id.String()) {
				continue
			}
			body, _ := json.Marshal(req)
			out, err := d.Handlers()[name](module.WithSender(ctx, from.String()), func(v any) error {
				return json.Unmarshal(body, v)
			})
			if err != nil {
				return err
			}
			b, _ := json.Marshal(out)
			return json.Unmarshal(b, resp)
		}
		return errors.New("unreachable")
	}
}

// add starts a node at addr. Servers accept connections; client-mode nodes
// don't, and are only found through their record.
func (m memNet) add(addr string, server bool, provides ...string) *DHT {
	seed := sha256.Sum256([]byte(addr))
	key := ed25519.NewKeyFromSeed(seed[:])
	d := New(Config{
		Key:   key,
		Name:  addr,
		Addrs: func() []string { return []string{addr} },
		DirectAddrs: func() []string {
			if server {
				return []string{addr}
			}
			return nil
		},
		Send:      m.sender(keyID(key.Public().(ed25519.PublicKey))),
		Bootstrap: func(context.Context) []string { return []string{"node-0"} },
		Refresh:   time.Minute,
		Republish: time.Minute,
		Provides:  func() []string { return provides },
	})
	m[addr] = d
	return d
}

func TestDHT(t *testing.T) {
	ctx := context.Background()
	net := memNet{}
	var servers []*DHT
	for i := range 30 {
		servers = append(servers, net.add(fmt.Sprintf("node-%d", i), true, fmt.Sprintf("cap-%d", i%3)))
	}
	client := net.add("client", false, "cap-1")

	// node-0 is everyone's bootstrap node, so it joins once others know it.
	joining := slices.Concat(servers[1:], servers[:1], []*DHT{client})
	for _, d := range joining {
		if err := d.refresh(ctx); err != nil {
			t.Fatalf("%s: %v", d.cfg.Name, err)
		}
	}
	for _, d := range joining {
		d.announce(ctx)
	}

	t.Run("finds servers", func(t *testing.T) {
		for _, target := range servers {
			found, ok, err := servers[7].FindNode(ctx, target.id)
			if err != nil || !ok || found.ID != target.id {
				t.Fatalf("FindNode(%s) = %v, %v, %v", target.cfg.Name, found, ok, err)
			}
		}
	})

	t.Run("finds client-mode nodes by record only", func(t *testing.T) {
		found, ok, err := servers[3].FindNode(ctx, client.id)
		if err != nil || !ok || !slices.Equal(found.Addrs, []string{"client"}) {
			t.Fatalf("FindNode(client) = %v, %v, %v", found, ok, err)
		}
		for _, d := range servers {
			if slices.ContainsFunc(d.Known(), func(c Contact) bool { return c.ID == client.id }) {
				t.Fatalf("%s routes through a client-mode node", d.cfg.Name)
			}
		}
	})

	t.Run("finds providers", func(t *testing.T) {
		found, err := servers[0].FindProviders(ctx, "cap-1")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range found {
			got = append(got, c.Name)
		}
		want := []string{"client"}
		for i := 1; i < 30; i += 3 {
			want = append(want, fmt.Sprintf("node-%d", i))
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("providers of cap-1 = %v, want %v", got, want)
		}
	})
}
