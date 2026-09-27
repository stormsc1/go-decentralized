// Package graph orders a chat channel's events. Each event names the latest
// events its author had seen, its parents, so a channel's events form a
// graph, and every node that has the same events sorts them the same way:
// parents before children, then by time, then by ID. See
// docs/design/identity-storage-chat.md, "Chat".
package graph

import (
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"go-decentralized/did"
)

// Event is something that happened in a channel.
type Event struct {
	// Channel is the channel's ID, unset on the event that creates it.
	Channel string `json:"channel,omitempty"`
	// Author is the DID of who made the event, and signed it.
	Author string          `json:"author"`
	Kind   string          `json:"kind"`
	Body   json.RawMessage `json:"body,omitempty"`
	// Parents are the IDs of the latest events the author had seen.
	Parents []string `json:"parents,omitempty"`
	// Time is when the event happened, by its author's clock.
	Time time.Time `json:"time"`
}

// Purpose is what authors sign events for.
const Purpose = "chat.event"

// ID returns the ID of the event data encodes: its SHA-256, in hex. A
// channel's ID is that of the event that creates it.
func ID(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Open verifies a signed event as of at (see did.Signed.Verify), and returns
// its ID and the event, whose author must be who signed it.
func Open(s did.Signed, at time.Time) (string, Event, error) {
	from, err := s.Verify(Purpose, at)
	if err != nil {
		return "", Event{}, err
	}
	var e Event
	if err := json.Unmarshal(s.Data, &e); err != nil {
		return "", Event{}, fmt.Errorf("event: %w", err)
	}
	if e.Author != from {
		return "", Event{}, fmt.Errorf("event by %s, signed by %s", e.Author, from)
	}
	return ID(s.Data), e, nil
}

// Graph is a channel's events, as far as a node has them. Events whose
// parents it doesn't have yet wait for them, out of the order.
type Graph struct {
	channel  string
	events   map[string]Event    // in the order: every ancestor is here
	children map[string][]string // of events in the order
	heads    map[string]bool     // events in the order without children
	waiting  map[string]Event    // for some ancestor
	needs    map[string][]string // events waiting for each missing parent
}

// New returns an empty graph of the channel with the given ID.
func New(channel string) *Graph {
	return &Graph{
		channel:  channel,
		events:   map[string]Event{},
		children: map[string][]string{},
		heads:    map[string]bool{},
		waiting:  map[string]Event{},
		needs:    map[string][]string{},
	}
}

// Add adds the event with the given ID, see ID. It returns the IDs of the
// events it adds to the order: this one and those that were waiting for it,
// or none if this one has to wait too, or was there already.
func (g *Graph) Add(id string, e Event) ([]string, error) {
	if err := g.check(id, e); err != nil {
		return nil, err
	}
	if g.Has(id) {
		return nil, nil
	}
	g.waiting[id] = e
	if missing := slices.DeleteFunc(slices.Clone(e.Parents), g.ordered); len(missing) > 0 {
		for _, p := range missing {
			g.needs[p] = append(g.needs[p], id)
		}
		return nil, nil
	}
	var added []string
	for ready := []string{id}; len(ready) > 0; ready = ready[1:] {
		id := ready[0]
		e := g.waiting[id]
		delete(g.waiting, id)
		g.events[id] = e
		for _, p := range e.Parents {
			g.children[p] = append(g.children[p], id)
			delete(g.heads, p)
		}
		g.heads[id] = true
		added = append(added, id)
		// Only the last of a waiting event's parents to arrive finds it
		// ready, so each is queued once.
		for _, w := range g.needs[id] {
			if !slices.ContainsFunc(g.waiting[w].Parents, func(p string) bool { return !g.ordered(p) }) {
				ready = append(ready, w)
			}
		}
		delete(g.needs, id)
	}
	return added, nil
}

// check checks that the event belongs in the graph: the event that creates
// the channel has no parents, and its ID is the channel's; every other event
// names the channel and its parents, once each.
func (g *Graph) check(id string, e Event) error {
	if len(e.Parents) == 0 {
		if id != g.channel || e.Channel != "" {
			return fmt.Errorf("event %.8s has no parents, but doesn't create channel %.8s", id, g.channel)
		}
		return nil
	}
	if e.Channel != g.channel {
		return fmt.Errorf("event %.8s is in channel %.8s, not %.8s", id, e.Channel, g.channel)
	}
	if slices.Contains(e.Parents, id) || len(slices.Compact(slices.Sorted(slices.Values(e.Parents)))) != len(e.Parents) {
		return fmt.Errorf("event %.8s names a parent twice, or itself", id)
	}
	return nil
}

// ordered reports whether the event id is in the order.
func (g *Graph) ordered(id string) bool {
	_, ok := g.events[id]
	return ok
}

// Has reports whether the graph has the event id, in the order or waiting.
func (g *Graph) Has(id string) bool {
	_, waiting := g.waiting[id]
	return waiting || g.ordered(id)
}

// Get returns the event id, if it's in the order.
func (g *Graph) Get(id string) (Event, bool) {
	e, ok := g.events[id]
	return e, ok
}

// Heads returns the IDs of the events in the order that no event names as a
// parent, sorted: the parents of the next event.
func (g *Graph) Heads() []string {
	return slices.Sorted(maps.Keys(g.heads))
}

// Missing returns the IDs of the events that waiting events need and the
// graph doesn't have, sorted: the ones to fetch.
func (g *Graph) Missing() []string {
	var ids []string
	for id := range g.needs {
		if _, ok := g.waiting[id]; !ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// Order returns the IDs of the events in the order: each after its parents,
// and otherwise by time, then by ID.
func (g *Graph) Order() []string {
	pending := map[string]int{} // parents not yet in the order returned
	ready := &byTime{g: g}
	for id, e := range g.events {
		if pending[id] = len(e.Parents); len(e.Parents) == 0 {
			heap.Push(ready, id)
		}
	}
	order := make([]string, 0, len(g.events))
	for ready.Len() > 0 {
		id := heap.Pop(ready).(string)
		order = append(order, id)
		for _, c := range g.children[id] {
			if pending[c]--; pending[c] == 0 {
				heap.Push(ready, c)
			}
		}
	}
	return order
}

// byTime is a heap of event IDs, earliest first, ties by ID.
type byTime struct {
	g   *Graph
	ids []string
}

func (h *byTime) Len() int      { return len(h.ids) }
func (h *byTime) Swap(i, j int) { h.ids[i], h.ids[j] = h.ids[j], h.ids[i] }
func (h *byTime) Push(x any)    { h.ids = append(h.ids, x.(string)) }

func (h *byTime) Less(i, j int) bool {
	a, b := h.ids[i], h.ids[j]
	if c := h.g.events[a].Time.Compare(h.g.events[b].Time); c != 0 {
		return c < 0
	}
	return strings.Compare(a, b) < 0
}

func (h *byTime) Pop() any {
	id := h.ids[len(h.ids)-1]
	h.ids = h.ids[:len(h.ids)-1]
	return id
}
