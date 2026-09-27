package network

import (
	"context"
	"time"

	"go-decentralized/module"
)

// maxTraces bounds how many traces a node keeps.
const maxTraces = 1000

// Trace records a call or stream a node made, for debugging.
type Trace struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"` // "call", "notify" or "stream"
	// Ref is the capability called, or the stream opened.
	Ref  string `json:"ref"`
	From string `json:"from"`
	// To is the ID of the node that answered, as its handshake proved.
	To       string        `json:"to,omitempty"`
	Addr     string        `json:"addr"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

// Traces returns what this node sent after since, oldest first.
func (n *Network) Traces(since time.Time) []Trace {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []Trace
	for _, t := range n.traces {
		if t.Time.After(since) {
			out = append(out, t)
		}
	}
	return out
}

// trace records a call or stream this node made.
func (n *Network) trace(ctx context.Context, kind, ref, addr, to string, start time.Time, err error) {
	if module.IsUntraced(ctx) {
		return
	}
	t := Trace{Time: time.Now(), Kind: kind, Ref: ref, From: n.id, To: to, Addr: addr, Duration: time.Since(start)}
	if err != nil {
		t.Error = err.Error()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.traces = append(n.traces, t)
	if len(n.traces) > maxTraces {
		n.traces = n.traces[len(n.traces)-maxTraces:]
	}
}
