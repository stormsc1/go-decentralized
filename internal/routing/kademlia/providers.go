package kademlia

import (
	"sync"
	"time"
)

// providers stores, per key, the records of the nodes that announced it.
// Nodes also announce their record under their own ID, which is how nodes
// outside routing tables (client mode) are found. Records expire unless
// re-announced.
type providers struct {
	ttl     time.Duration
	mu      sync.Mutex
	records map[ID]map[ID]Record
}

func newProviders(ttl time.Duration) *providers {
	return &providers{ttl: ttl, records: map[ID]map[ID]Record{}}
}

// add stores r, a verified record, as a provider of key.
func (p *providers) add(key ID, r Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.records[key] == nil {
		p.records[key] = map[ID]Record{}
	}
	keepNewest(p.records[key], r)
}

// get returns the unexpired records of key's providers.
func (p *providers) get(key ID) []Record {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Record
	for id, r := range p.records[key] {
		if !r.fresh(p.ttl) {
			delete(p.records[key], id)
			continue
		}
		out = append(out, r)
	}
	return out
}

// nodeRecords returns the unexpired records nodes announced under their own
// ID.
func (p *providers) nodeRecords() []Record {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Record
	for key, records := range p.records {
		if r, ok := records[key]; ok && r.fresh(p.ttl) {
			out = append(out, r)
		}
	}
	return out
}
