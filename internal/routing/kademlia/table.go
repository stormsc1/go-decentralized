package kademlia

import (
	"slices"
	"sync"
)

// K is the bucket size and the number of closest nodes a lookup converges on.
const K = 20

// Contact is how a node is reached. Addrs are tried in order. Nodes without
// any (client mode, e.g. the CLI) can query the DHT but are never added to
// routing tables.
type Contact struct {
	ID    ID       `json:"id"`
	Addrs []string `json:"addrs,omitempty"`
	Name  string   `json:"name,omitempty"`
}

// Table is the Kademlia routing table: one bucket per shared-prefix length.
type Table struct {
	self    ID
	mu      sync.Mutex
	buckets [len(ID{}) * 8][]Contact
}

func NewTable(self ID) *Table { return &Table{self: self} }

// Add records that c was seen, moving it to the tail (most recently seen) of
// its bucket. Full buckets keep their long-lived contacts, as Kademlia
// prefers uptime; dead contacts are dropped via Remove when an RPC fails.
func (t *Table) Add(c Contact) {
	if c.ID == t.self || len(c.Addrs) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	b := &t.buckets[commonPrefixLen(t.self, c.ID)]
	*b = slices.DeleteFunc(*b, func(e Contact) bool { return e.ID == c.ID })
	if len(*b) < K {
		*b = append(*b, c)
	}
}

func (t *Table) Remove(id ID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := &t.buckets[commonPrefixLen(t.self, id)]
	*b = slices.DeleteFunc(*b, func(e Contact) bool { return e.ID == id })
}

// Closest returns up to n known contacts ordered by distance to target.
func (t *Table) Closest(target ID, n int) []Contact {
	all := t.All()
	sortByDistance(all, target)
	return all[:min(n, len(all))]
}

func (t *Table) All() []Contact {
	t.mu.Lock()
	defer t.mu.Unlock()
	var all []Contact
	for _, b := range t.buckets {
		all = append(all, b...)
	}
	return all
}

func (t *Table) Len() int { return len(t.All()) }
