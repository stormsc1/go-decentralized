package node

import (
	"bytes"
	"encoding/json"
	"slices"

	"go-decentralized/module"
)

// Event is a module's event, as subscribers get it.
type Event struct {
	// Ref names the event: "<module>.<event>".
	Ref  string          `json:"ref"`
	Body json.RawMessage `json:"body"`
	// To is who the event is for, as DIDs: everyone if empty.
	To []string `json:"to,omitempty"`
}

// behind is how many events a subscriber may fall behind by before the node
// ends its subscription.
const behind = 64

// Subscription is a local tool's subscription to events, see Subscribe.
type Subscription struct {
	n      *Node
	refs   []string
	person string // who the subscriber is, or "" for nobody
	all    bool   // sees events for anyone
	events chan Event
}

// Events delivers the events subscribed to, in the order they happened. It
// closes when the subscription ends: once closed, or once the subscriber
// falls too far behind and so would miss events. Subscribers then catch up
// by calling capabilities.
func (s *Subscription) Events() <-chan Event { return s.events }

// Close ends the subscription.
func (s *Subscription) Close() { s.n.unsubscribe(s) }

// Subscribe subscribes to the events refs name, e.g. "chat.message", for
// the node's own tools, which see events for anyone.
func (n *Node) Subscribe(refs ...string) (*Subscription, error) {
	return n.subscribe(refs, "", true)
}

// SubscribeAs subscribes to events like Subscribe, for the person with the
// given DID: they get the events for everyone, and those for them. With no
// person, only the events for everyone.
func (n *Node) SubscribeAs(person string, refs ...string) (*Subscription, error) {
	return n.subscribe(refs, person, false)
}

func (n *Node) subscribe(refs []string, person string, all bool) (*Subscription, error) {
	if len(refs) == 0 {
		return nil, module.Errorf(module.CodeInvalidArgument, "no events to subscribe to")
	}
	n.mu.RLock()
	for _, ref := range refs {
		if n.events[ref] == nil {
			n.mu.RUnlock()
			return nil, module.Errorf(module.CodeNotFound, "no event %s", ref)
		}
	}
	n.mu.RUnlock()
	s := &Subscription{n: n, refs: refs, person: person, all: all, events: make(chan Event, behind)}
	n.smu.Lock()
	n.subs[s] = struct{}{}
	n.smu.Unlock()
	return s, nil
}

func (n *Node) unsubscribe(s *Subscription) {
	n.smu.Lock()
	defer n.smu.Unlock()
	if _, ok := n.subs[s]; ok {
		delete(n.subs, s)
		close(s.events)
	}
}

// wants reports whether the subscription gets e.
func (s *Subscription) wants(e Event) bool {
	if !slices.Contains(s.refs, e.Ref) {
		return false
	}
	return len(e.To) == 0 || s.all || (s.person != "" && slices.Contains(e.To, s.person))
}

// emit tells the subscribers of a module's event called name, once body
// matches the event's schema: those subscribed for the people in to, or
// everyone if to is empty.
func (n *Node) emit(from, name string, body json.RawMessage, to []string) error {
	ref := from + "." + name
	n.mu.RLock()
	schema := n.events[ref]
	n.mu.RUnlock()
	if schema == nil {
		return module.Errorf(module.CodeNotFound, "module %s has no event %q", from, name)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = json.RawMessage("{}")
	}
	if err := validate(schema, body); err != nil {
		return module.Errorf(module.CodeInvalidArgument, "%s: %v", ref, err)
	}
	// On one line, as event streams carry them.
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		return module.Errorf(module.CodeInvalidArgument, "%s: %v", ref, err)
	}
	e := Event{Ref: ref, Body: compact.Bytes(), To: to}
	n.smu.Lock()
	defer n.smu.Unlock()
	for s := range n.subs {
		if !s.wants(e) {
			continue
		}
		select {
		case s.events <- e:
		default:
			delete(n.subs, s)
			close(s.events)
		}
	}
	return nil
}
