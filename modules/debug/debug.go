// Package debug reports on nodes, for tools like the network explorer
// (web/). Nodes running it announce its capabilities like any others, so a
// tool finds every node it can debug through discovery, by capability, and
// asks each for its report over the network.
package debug

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"go-decentralized/internal/api"
	"go-decentralized/internal/module"
	"go-decentralized/modules/debug/capabilities"
	"go-decentralized/modules/discovery/kademlia"
)

const Name = "debug"

// Messages debug sends between nodes. The nodes answering them are the
// providers of the debug.report capability.
const (
	msgReport = "report"
	msgTraces = "traces"
	// msgPing is answered by every node's network.
	msgPing = "network.ping"
)

// Report describes a node's state.
type Report struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Addrs []string `json:"addrs"`
	// Direct reports whether the node accepts direct connections. Nodes that
	// don't are in client mode, reachable through relay addresses if any.
	Direct bool `json:"direct"`
	// Observed are the public IPs other nodes see the node at. Nodes behind
	// the same NAT share them.
	Observed []string `json:"observed,omitempty"`
	// Network is the state of the node's network, e.g. its relays.
	Network any `json:"network,omitempty"`
	// Modules maps each module to its capabilities and, for modules that
	// implement module.Inspector, its state.
	Modules map[string]ModuleReport `json:"modules"`
	// Error is set by map_network for nodes it couldn't reach.
	Error string `json:"error,omitempty"`
}

type ModuleReport struct {
	Capabilities []string `json:"capabilities"`
	State        any      `json:"state,omitempty"`
}

type pingResult struct {
	Addr string `json:"addr"`
	RTT  string `json:"rtt"`
}

type Module struct {
	env module.Env

	mu    sync.Mutex
	nodes []kademlia.Contact // nodes running the debug module, see debugNodes
	found time.Time          // when nodes was looked up
}

func New(_ func(any) error, env module.Env) (module.Module, error) {
	return &Module{env: env}, nil
}

func (m *Module) Name() string { return Name }

func (m *Module) Capabilities() []module.Capability {
	return []module.Capability{
		&capabilities.Report{Build: func() any { return m.report() }},
		&capabilities.MapNetwork{Map: func(ctx context.Context) (any, error) { return m.mapNetwork(ctx) }},
		&capabilities.Traffic{Collect: func(ctx context.Context, since time.Time) (any, error) { return m.traffic(ctx, since) }},
		&capabilities.PingNode{Ping: m.ping},
	}
}

func (m *Module) Messages() map[string]module.Handler {
	return map[string]module.Handler{
		msgReport: module.HandlerFor(func(context.Context, struct{}) (Report, error) {
			return m.report(), nil
		}),
		msgTraces: module.HandlerFor(func(_ context.Context, req tracesRequest) ([]api.Trace, error) {
			return m.env.Network.Traces(req.Since), nil
		}),
	}
}

func (m *Module) report() Report {
	r := Report{
		ID:       m.env.NodeID,
		Name:     m.env.NodeName,
		Addrs:    m.env.Network.Addrs(),
		Direct:   len(m.env.Network.DirectAddrs()) > 0,
		Observed: m.env.Network.Observed(),
		Network:  m.env.Network.Inspect(),
		Modules:  map[string]ModuleReport{},
	}
	for _, mod := range m.env.Registry.Modules() {
		var mr ModuleReport
		for _, c := range mod.Capabilities() {
			mr.Capabilities = append(mr.Capabilities, c.Name())
		}
		if i, ok := mod.(module.Inspector); ok {
			mr.State = i.Inspect()
		}
		r.Modules[mod.Name()] = mr
	}
	return r
}

// mapNetwork reports on every node running the debug module.
func (m *Module) mapNetwork(ctx context.Context) ([]Report, error) {
	ctx = module.Untraced(ctx) // keep our own polling out of the traffic
	nodes, err := m.debugNodes(ctx)
	if err != nil {
		return nil, err
	}
	reports := make([]Report, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() {
			if err := m.env.Send(ctx, peerOf(n), Name+"."+msgReport, struct{}{}, &reports[i]); err != nil {
				reports[i] = Report{ID: n.ID.String(), Name: n.Name, Addrs: n.Addrs, Error: err.Error()}
			}
		})
	}
	wg.Wait()
	slices.SortFunc(reports, func(a, b Report) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return reports, nil
}

// debugNodes finds the nodes running the debug module, as providers of
// debug.report. It reuses the answer for a few seconds, as tools poll.
func (m *Module) debugNodes(ctx context.Context) ([]kademlia.Contact, error) {
	m.mu.Lock()
	nodes, fresh := m.nodes, time.Since(m.found) < 10*time.Second
	m.mu.Unlock()
	if fresh {
		return nodes, nil
	}
	found, err := m.env.Registry.Invoke(ctx, "discovery.find_capability_providers", module.Args{"capability": Name + ".report"})
	if err != nil {
		return nil, err
	}
	nodes, ok := found.([]kademlia.Contact)
	if !ok {
		return nil, fmt.Errorf("unexpected providers %T", found)
	}
	m.mu.Lock()
	m.nodes, m.found = nodes, time.Now()
	m.mu.Unlock()
	return nodes, nil
}

// ping pings the node with the given ID, through a relay if needed, and
// reports the address that answered first. TLS proves it's the right node.
func (m *Module) ping(ctx context.Context, id string) (any, error) {
	found, err := m.env.Registry.Invoke(ctx, "discovery.find_node_by_id", module.Args{"id": id})
	if err != nil {
		return nil, err
	}
	node, ok := found.(kademlia.Contact)
	if !ok {
		return nil, fmt.Errorf("unexpected node %T", found)
	}
	var errs []error
	for _, addr := range node.Addrs {
		start := time.Now()
		err := m.env.Send(ctx, api.Peer{ID: node.ID.String(), Addrs: []string{addr}}, msgPing, struct{}{}, &struct{}{})
		if err == nil {
			return pingResult{Addr: addr, RTT: time.Since(start).Round(time.Microsecond).String()}, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

func peerOf(c kademlia.Contact) api.Peer {
	return api.Peer{ID: c.ID.String(), Addrs: c.Addrs}
}
