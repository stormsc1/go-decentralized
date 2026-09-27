package network

import (
	"context"
	"time"

	"go-decentralized/internal/api"
)

// maxTraces bounds how many traces a node keeps.
const maxTraces = 1000

type noTraceKey struct{}

// WithoutTrace marks ctx so that what's sent with it isn't traced, e.g. the
// debugging tools' own traffic.
func WithoutTrace(ctx context.Context) context.Context {
	return context.WithValue(ctx, noTraceKey{}, true)
}

// Traces returns what this node sent after since, oldest first.
func (n *Network) Traces(since time.Time) []api.Trace {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []api.Trace
	for _, t := range n.traces {
		if t.Time.After(since) {
			out = append(out, t)
		}
	}
	return out
}

// trace records a message or stream this node sent.
func (n *Network) trace(ctx context.Context, kind, name, addr, to string, start time.Time, err error) {
	if ctx.Value(noTraceKey{}) != nil {
		return
	}
	t := api.Trace{Time: time.Now(), Kind: kind, Name: name, From: n.id, To: to, Addr: addr, Duration: time.Since(start)}
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
