package debug

import (
	"context"
	"slices"
	"sync"
	"time"

	"go-decentralized/module"
)

// Trace records a call or stream a node made, as node.traces returns it.
type Trace struct {
	Time     time.Time     `json:"time"`
	Kind     string        `json:"kind"`
	Ref      string        `json:"ref"`
	From     string        `json:"from"`
	To       string        `json:"to,omitempty"`
	Addr     string        `json:"addr"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

type since struct {
	Since time.Time `json:"since"`
}

type traces struct {
	Traces []Trace `json:"traces"`
}

// traces returns what this node sent after since, oldest first.
func (m *Module) traces(ctx context.Context, in since) (traces, error) {
	var out traces
	err := m.env.Call(ctx, "node.traces", in, &out)
	return out, err
}

// traffic collects what every node running the debug module sent after
// since, oldest first.
func (m *Module) traffic(ctx context.Context, in since) (traces, error) {
	ctx = module.Untraced(ctx) // keep our own polling out of the traffic
	nodes, err := m.debugNodes(ctx)
	if err != nil {
		return traces{}, err
	}
	all, err := m.traces(ctx, in)
	if err != nil {
		return traces{}, err
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range nodes {
		if n.ID == m.env.NodeID {
			continue // already have ours
		}
		wg.Go(func() {
			var theirs traces
			if err := m.env.CallNode(ctx, n.ID, Name+".traces", in, &theirs); err != nil {
				return
			}
			mu.Lock()
			all.Traces = append(all.Traces, theirs.Traces...)
			mu.Unlock()
		})
	}
	wg.Wait()
	slices.SortFunc(all.Traces, func(a, b Trace) int { return a.Time.Compare(b.Time) })
	return all, nil
}
