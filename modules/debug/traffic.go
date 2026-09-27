package debug

import (
	"context"
	"slices"
	"sync"
	"time"

	"go-decentralized/internal/network"
)

// msgTraces asks a node for the traces of what it sent after Since.
const msgTraces = "debug.traces"

// maxTraces bounds how many traces a node keeps.
const maxTraces = 1000

type tracesRequest struct {
	Since time.Time `json:"since"`
}

func (m *Module) record(t network.Trace) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.traces = append(m.traces, t)
	if len(m.traces) > maxTraces {
		m.traces = m.traces[len(m.traces)-maxTraces:]
	}
}

func (m *Module) tracesSince(since time.Time) []network.Trace {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []network.Trace
	for _, t := range m.traces {
		if t.Time.After(since) {
			out = append(out, t)
		}
	}
	return out
}

// traffic collects what every node running the debug module sent after
// since, oldest first.
func (m *Module) traffic(ctx context.Context, since time.Time) ([]network.Trace, error) {
	ctx = network.WithoutTrace(ctx) // keep our own polling out of the traffic
	nodes, err := m.debugNodes(ctx)
	if err != nil {
		return nil, err
	}
	all := m.tracesSince(since)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range nodes {
		if n.ID.String() == m.env.NodeID {
			continue // already have ours
		}
		wg.Go(func() {
			var traces []network.Trace
			if err := m.env.Network.Send(ctx, n.Addrs, msgTraces, tracesRequest{Since: since}, &traces); err != nil {
				return
			}
			mu.Lock()
			all = append(all, traces...)
			mu.Unlock()
		})
	}
	wg.Wait()
	slices.SortFunc(all, func(a, b network.Trace) int { return a.Time.Compare(b.Time) })
	return all, nil
}
