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
}

// behind is how many events a subscriber may fall behind by before the node
// ends its subscription.
const behind = 64

// Subscription is a local tool's subscription to events, see Subscribe.
type Subscription struct {
	n      *Node
	refs   []string
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
// local tools such as apps.
func (n *Node) Subscribe(refs ...string) (*Subscription, error) {
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
	s := &Subscription{n: n, refs: refs, events: make(chan Event, behind)}
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

// emit tells the subscribers of a module's event called name, once body
// matches the event's schema.
func (n *Node) emit(from, name string, body json.RawMessage) error {
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
	e := Event{Ref: ref, Body: compact.Bytes()}
	n.smu.Lock()
	defer n.smu.Unlock()
	for s := range n.subs {
		if !slices.Contains(s.refs, ref) {
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
