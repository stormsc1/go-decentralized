package network

import (
	"context"
	"time"
)

// Trace records a message or stream this node sent, for debugging.
type Trace struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"` // "message" or "stream"
	Name string    `json:"name"`
	From string    `json:"from"`
	// To is the ID of the node that answered, if any did.
	To       string        `json:"to,omitempty"`
	Addr     string        `json:"addr"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

type noTraceKey struct{}

// WithoutTrace marks ctx so that what's sent with it isn't traced, e.g. the
// debugging tools' own traffic.
func WithoutTrace(ctx context.Context) context.Context {
	return context.WithValue(ctx, noTraceKey{}, true)
}

// OnTrace calls f with a Trace of every message and stream this node sends.
func (n *Network) OnTrace(f func(Trace)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.tracers = append(n.tracers, f)
}

func (n *Network) trace(ctx context.Context, kind, name, addr, to string, start time.Time, err error) {
	n.mu.Lock()
	tracers := n.tracers
	n.mu.Unlock()
	if len(tracers) == 0 || ctx.Value(noTraceKey{}) != nil {
		return
	}
	t := Trace{Time: time.Now(), Kind: kind, Name: name, From: n.cfg.ID, To: to, Addr: addr, Duration: time.Since(start)}
	if err != nil {
		t.Error = err.Error()
	}
	for _, f := range tracers {
		f(t)
	}
}
