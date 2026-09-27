package kademlia

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"time"

	"go-decentralized/internal/api"
	"go-decentralized/internal/module"
)

// alpha is the number of messages a lookup keeps in flight at once.
const alpha = 3

type Config struct {
	// Key is this node's key: its ID is derived from it, and it signs the
	// node's record.
	Key  ed25519.PrivateKey
	Name string
	// Addrs returns every address this node can be reached at, including
	// indirect ones such as relays. They are published in its record.
	Addrs func() []string
	// DirectAddrs returns the addresses this node accepts connections on.
	// Without any, the node runs in client mode: it isn't added to routing
	// tables, but can still be found through its record.
	DirectAddrs func() []string
	// Send carries the DHT's messages, named as in Handlers.
	Send module.SendFunc
	// Bootstrap returns addresses (host:port) of nodes to join the network
	// through. It is called whenever the routing table is empty.
	Bootstrap func(ctx context.Context) []string
	// Refresh is how often the node refreshes its routing table and
	// re-announces its record. Records live for 3x Refresh.
	Refresh time.Duration
	// Provides returns the keys (capability refs) this node announces.
	Provides func() []string
}

type DHT struct {
	cfg       Config
	id        ID
	table     *Table
	providers *providers
}

func New(cfg Config) *DHT {
	id := keyID(cfg.Key.Public().(ed25519.PublicKey))
	return &DHT{
		cfg:       cfg,
		id:        id,
		table:     NewTable(id),
		providers: newProviders(3 * cfg.Refresh),
	}
}

// self is the contact this node sends with every message.
func (d *DHT) self() Contact {
	return Contact{ID: d.id, Addrs: d.cfg.DirectAddrs(), Name: d.cfg.Name}
}

// record is this node's current record.
func (d *DHT) record() Record {
	return newRecord(d.cfg.Key, d.cfg.Name, d.cfg.Addrs())
}

// Run keeps the node joined to the network until ctx is done. It refreshes
// every Refresh, and straight away when the table is empty or this node's
// addresses change, so peers learn new addresses quickly.
func (d *DHT) Run(ctx context.Context) {
	var last time.Time
	var lastAddrs []string
	for {
		addrs := d.cfg.Addrs()
		if d.table.Len() == 0 || time.Since(last) >= d.cfg.Refresh || !slices.Equal(addrs, lastAddrs) {
			if err := d.refresh(ctx); err != nil {
				slog.Warn("discovery: refresh failed", "err", err)
			} else {
				last, lastAddrs = time.Now(), addrs
			}
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
	if self := d.record(); len(self.Addrs) > 0 {
		known[d.id] = self.Contact()
	}
	nodes := slices.Collect(maps.Values(known))
	sortByDistance(nodes, d.id)
	return nodes, nil
}

// FindNode locates the node with the given ID.
func (d *DHT) FindNode(ctx context.Context, id ID) (Contact, bool, error) {
	if id == d.id {
		return d.record().Contact(), true, nil
	}
	if err := d.ensureJoined(ctx); err != nil {
		return Contact{}, false, err
	}
	// Nodes announce their record under their own ID, which finds nodes
	// outside routing tables too.
	closest, records := d.lookup(ctx, id, msgFindProviders)
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
	_, records := d.lookup(ctx, k, msgFindProviders)
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

// refresh joins via the bootstrap nodes if needed, looks up our own ID to
// fill the routing table, and re-announces our record.
func (d *DHT) refresh(ctx context.Context) error {
	if d.table.Len() == 0 {
		for _, addr := range d.cfg.Bootstrap(ctx) {
			if _, err := d.call(ctx, api.Peer{Addrs: []string{addr}}, msgFindNode, request{Target: d.id}); err != nil {
				slog.Debug("discovery: bootstrap failed", "addr", addr, "err", err)
			}
		}
		if d.table.Len() == 0 {
			return errors.New("no bootstrap node reachable")
		}
	}
	d.lookup(ctx, d.id, msgFindNode)
	d.announce(ctx)
	return nil
}

// announce stores this node's record on the K nodes closest to its own ID,
// so it can be found by ID, and to each key it provides.
func (d *DHT) announce(ctx context.Context) {
	rec := d.record()
	if len(rec.Addrs) == 0 {
		return // unreachable: nothing to announce
	}
	keys := []ID{d.id}
	for _, key := range d.cfg.Provides() {
		keys = append(keys, HashKey(key))
	}
	for _, k := range keys {
		d.providers.add(k, rec)
		closest, _ := d.lookup(ctx, k, msgFindNode)
		for _, c := range closest {
			if _, err := d.call(ctx, peerOf(c), msgAddProvider, request{Target: k, Record: &rec}); err != nil {
				d.table.Remove(c.ID)
			}
		}
	}
}

// lookup is Kademlia's iterative search: it keeps querying the alpha closest
// contacts not yet asked, merging what they return, until the K closest
// known contacts have all answered. It returns those contacts and, for
// find_providers, the valid provider records seen along the way.
func (d *DHT) lookup(ctx context.Context, target ID, msg string) ([]Contact, map[ID]Record) {
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
				resp, err := d.call(ctx, peerOf(c), msg, request{Target: target})
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
				if r.valid(d.providers.ttl) {
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
