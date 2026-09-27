package debug

import (
	"context"
	"slices"
	"sync"
	"time"

	"go-decentralized/internal/api"
	"go-decentralized/internal/module"
)

type tracesRequest struct {
	Since time.Time `json:"since"`
}

// traffic collects what every node running the debug module sent after
// since, oldest first.
func (m *Module) traffic(ctx context.Context, since time.Time) ([]api.Trace, error) {
	ctx = module.Untraced(ctx) // keep our own polling out of the traffic
	nodes, err := m.debugNodes(ctx)
	if err != nil {
		return nil, err
	}
	all := m.env.Network.Traces(since)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range nodes {
		if n.ID.String() == m.env.NodeID {
			continue // already have ours
		}
		wg.Go(func() {
			var traces []api.Trace
			if err := m.env.Send(ctx, peerOf(n), Name+"."+msgTraces, tracesRequest{Since: since}, &traces); err != nil {
				return
			}
			mu.Lock()
			all = append(all, traces...)
			mu.Unlock()
		})
	}
	wg.Wait()
	slices.SortFunc(all, func(a, b api.Trace) int { return a.Time.Compare(b.Time) })
	return all, nil
}
