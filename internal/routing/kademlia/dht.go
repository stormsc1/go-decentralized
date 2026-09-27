package kademlia

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"time"

	"go-decentralized/internal/network"
)

// alpha is the number of calls a lookup keeps in flight at once.
const alpha = 3

// SignFunc signs data for purpose with this node's key, see module.Sign.
type SignFunc func(ctx context.Context, purpose string, data []byte) ([]byte, error)

type Config struct {
	// PublicKey is this node's key: its ID is derived from it.
	PublicKey ed25519.PublicKey
	// Sign signs the node's record.
	Sign SignFunc
	Name string
	// Addrs returns every address this node can be reached at, including
	// indirect ones such as relays. They are published in its record.
	Addrs func() []string
	// DirectAddrs returns the addresses this node accepts connections on.
	// Without any, the node runs in client mode: it isn't added to routing
	// tables, but can still be found through its record.
	DirectAddrs func() []string
	// Call calls a DHT capability, named as in Handlers, on another node.
	Call func(ctx context.Context, to network.Peer, name string, in, out any) error
	// Bootstrap returns addresses (host:port) of nodes to join the network
	// through. It is called whenever the routing table is empty.
	Bootstrap func(ctx context.Context) []string
	// Refresh is how often the node refreshes its routing table.
	Refresh time.Duration
	// Republish is how often the node re-announces its record and what it
	// provides. Records live for 3x Republish.
	Republish time.Duration
	// Provides returns the keys (capability refs) this node announces.
	Provides func() []string
	// Changed, if set, is told the routing table's contents whenever they
	// change, from Run.
	Changed func(known []Contact)
}

type DHT struct {
	cfg       Config
	id        ID
	table     *Table
	providers *providers
}

func New(cfg Config) *DHT {
	id := keyID(cfg.PublicKey)
	return &DHT{
		cfg:       cfg,
		id:        id,
		table:     NewTable(id),
		providers: newProviders(3 * cfg.Republish),
	}
}

// self is the contact this node sends with every call.
func (d *DHT) self() Contact {
	return Contact{ID: d.id, Addrs: d.cfg.DirectAddrs(), Name: d.cfg.Name}
}

// selfRecord is this node as its record describes it.
func (d *DHT) selfRecord() Contact {
	return Contact{ID: d.id, Addrs: d.cfg.Addrs(), Name: d.cfg.Name}
}

// Run keeps the node joined to the network until ctx is done. It refreshes
// the routing table every Refresh and republishes every Republish, and does
// both straight away when it joins or this node's addresses change, so peers
// learn new addresses quickly.
func (d *DHT) Run(ctx context.Context) {
	var refreshed, announced time.Time
	var announcedAddrs []string
	var told []Contact // the table as Changed last saw it
	for {
		addrs := d.cfg.Addrs()
		changed := !slices.Equal(addrs, announcedAddrs)
		if d.table.Len() == 0 || changed || time.Since(refreshed) >= d.cfg.Refresh {
			if err := d.refresh(ctx); err != nil {
				slog.Warn("routing: refresh failed", "err", err)
			} else {
				refreshed = time.Now()
			}
		}
		if d.table.Len() > 0 && (changed || time.Since(announced) >= d.cfg.Republish) {
			d.announce(ctx)
			announced, announcedAddrs = time.Now(), addrs
		}
		if known := d.table.All(); d.cfg.Changed != nil && !sameNodes(known, told) {
			d.cfg.Changed(known)
			told = known
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// Known returns the nodes in the routing table, without any lookups.
func (d *DHT) Known() []Contact { return d.table.All() }

// sameNodes reports whether a and b hold the same nodes.
func sameNodes(a, b []Contact) bool {
	if len(a) != len(b) {
		return false
	}
	ids := map[ID]bool{}
	for _, c := range a {
		ids[c.ID] = true
	}
	for _, c := range b {
		if !ids[c.ID] {
			return false
		}
	}
	return true
}

// Local returns what this node knows of the node id, without any lookups:
// its record, which has every address it can be reached at, or else its
// routing table contact.
func (d *DHT) Local(id ID) (Contact, bool) {
	if id == d.id {
		return d.selfRecord(), true
	}
	for _, r := range d.providers.nodeRecords() {
		if r.ID() == id {
			return r.Contact(), true
		}
	}
	for _, c := range d.table.All() {
		if c.ID == id {
			return c, true
		}
	}
	return Contact{}, false
}

// Nodes returns the nodes this node knows: its routing table, the records it
// stores (which include client-mode nodes) and itself.
func (d *DHT) Nodes(ctx context.Context) ([]Contact, error) {
	if err := d.ensureJoined(ctx); err != nil {
		return nil, err
	}
	known := map[ID]Contact{}
	for _, c := range d.table.All() {
		known[c.ID] = c
	}
	for _, r := range d.providers.nodeRecords() {
		known[r.ID()] = r.Contact()
	}
	if self := d.selfRecord(); len(self.Addrs) > 0 {
		known[d.id] = self
	}
	nodes := slices.Collect(maps.Values(known))
	sortByDistance(nodes, d.id)
	return nodes, nil
}

// FindNode locates the node with the given ID.
func (d *DHT) FindNode(ctx context.Context, id ID) (Contact, bool, error) {
	if id == d.id {
		return d.selfRecord(), true, nil
	}
	if err := d.ensureJoined(ctx); err != nil {
		return Contact{}, false, err
	}
	// Nodes announce their record under their own ID, which finds nodes
	// outside routing tables too.
	closest, records := d.lookup(ctx, id, capFindProviders)
	if r, ok := records[id]; ok {
		return r.Contact(), true, nil
	}
	if len(closest) > 0 && closest[0].ID == id {
		return closest[0], true, nil
	}
	return Contact{}, false, nil
}

// FindProviders returns the nodes that announced the given key.
func (d *DHT) FindProviders(ctx context.Context, key string) ([]Contact, error) {
	if err := d.ensureJoined(ctx); err != nil {
		return nil, err
	}
	k := HashKey(key)
	_, records := d.lookup(ctx, k, capFindProviders)
	for _, r := range d.providers.get(k) {
		keepNewest(records, r)
	}
	var found []Contact
	for _, r := range records {
		found = append(found, r.Contact())
	}
	return found, nil
}

func (d *DHT) ensureJoined(ctx context.Context) error {
	if d.table.Len() > 0 {
		return nil
	}
	return d.refresh(ctx)
}

// refresh joins via the bootstrap nodes if needed and looks up our own ID
// to fill the routing table.
func (d *DHT) refresh(ctx context.Context) error {
	if d.table.Len() == 0 {
		for _, addr := range d.cfg.Bootstrap(ctx) {
			if _, err := d.call(ctx, network.Peer{Addrs: []string{addr}}, capFindNode, request{Target: d.id}); err != nil {
				slog.Debug("routing: bootstrap failed", "addr", addr, "err", err)
			}
		}
		if d.table.Len() == 0 {
			return errors.New("no bootstrap node reachable")
		}
	}
	d.lookup(ctx, d.id, capFindNode)
	return nil
}

// announce stores this node's record on the K nodes closest to its own ID,
// so it can be found by ID, and to each key it provides.
func (d *DHT) announce(ctx context.Context) {
	addrs := d.cfg.Addrs()
	if len(addrs) == 0 {
		return // unreachable: nothing to announce
	}
	rec, err := newRecord(ctx, d.cfg.Sign, d.cfg.PublicKey, d.cfg.Name, addrs)
	if err != nil {
		slog.Warn("routing: can't sign record", "err", err)
		return
	}
	keys := []ID{d.id}
	for _, key := range d.cfg.Provides() {
		keys = append(keys, HashKey(key))
	}
	for _, k := range keys {
		d.providers.add(k, rec)
		closest, _ := d.lookup(ctx, k, capFindNode)
		for _, c := range closest {
			if _, err := d.call(ctx, peerOf(c), capAddProvider, request{Target: k, Record: &rec}); err != nil {
				d.table.Remove(c.ID)
			}
		}
	}
}

// lookup is Kademlia's iterative search: it keeps querying the alpha closest
// contacts not yet asked, merging what they return, until the K closest
// known contacts have all answered. It returns those contacts and, for
// dht_find_providers, the valid provider records seen along the way.
func (d *DHT) lookup(ctx context.Context, target ID, capability string) ([]Contact, map[ID]Record) {
	shortlist := d.table.Closest(target, K)
	seen := map[ID]bool{d.id: true}
	for _, c := range shortlist {
		seen[c.ID] = true
	}
	asked := map[ID]bool{}
	records := map[ID]Record{}

	type result struct {
		from Contact
		resp response
		err  error
	}
	for {
		var batch []Contact
		for _, c := range shortlist {
			if !asked[c.ID] && len(batch) < alpha {
				batch = append(batch, c)
			}
		}
		if len(batch) == 0 {
			break
		}

		results := make(chan result, len(batch))
		for _, c := range batch {
			asked[c.ID] = true
			go func() {
				resp, err := d.call(ctx, peerOf(c), capability, request{Target: target})
				results <- result{c, resp, err}
			}()
		}
		for range batch {
			res := <-results
			if res.err != nil {
				d.table.Remove(res.from.ID)
				shortlist = slices.DeleteFunc(shortlist, func(c Contact) bool { return c.ID == res.from.ID })
				continue
			}
			for _, r := range res.resp.Providers {
				if r, ok := r.verified(d.providers.ttl); ok {
					keepNewest(records, r)
				}
			}
			for _, c := range res.resp.Contacts {
				if !seen[c.ID] {
					seen[c.ID] = true
					shortlist = append(shortlist, c)
				}
			}
		}
		sortByDistance(shortlist, target)
		shortlist = shortlist[:min(K, len(shortlist))]
	}
	return shortlist, records
}
