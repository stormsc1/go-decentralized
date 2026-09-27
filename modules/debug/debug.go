// Package debug reports on nodes, for tools like the network explorer
// (web/). Nodes running it announce debug.report like any capability, so a
// tool finds every node it can debug through discovery, and asks each for
// its report over the network.
package debug

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"go-decentralized/module"
)

const Name = "debug"

//go:embed module.yaml
var manifest []byte

// Report describes a node's state.
type Report struct {
	ID       string                  `json:"id"`
	Name     string                  `json:"name"`
	Addrs    []string                `json:"addrs"`
	Direct   bool                    `json:"direct"`
	Observed []string                `json:"observed,omitempty"`
	Network  json.RawMessage         `json:"network,omitempty"`
	Modules  map[string]ModuleReport `json:"modules"`
	Error    string                  `json:"error,omitempty"`
}

type ModuleReport struct {
	Capabilities []string        `json:"capabilities"`
	State        json.RawMessage `json:"state,omitempty"`
}

// node is a node as discovery finds it.
type node struct {
	ID    string   `json:"id"`
	Name  string   `json:"name,omitempty"`
	Addrs []string `json:"addrs,omitempty"`
}

type Module struct {
	env module.Env

	mu    sync.Mutex
	nodes []node    // nodes running the debug module, see debugNodes
	found time.Time // when nodes was looked up
}

func New(_ func(any) error, env module.Env) (module.Module, error) {
	return &Module{env: env}, nil
}

func (m *Module) Manifest() module.Manifest { return module.MustParseManifest(manifest) }

func (m *Module) Handlers() map[string]module.Handler {
	return map[string]module.Handler{
		"report":      module.HandlerFor(func(ctx context.Context, _ struct{}) (Report, error) { return m.report(ctx) }),
		"traces":      module.HandlerFor(m.traces),
		"map_network": module.HandlerFor(m.mapNetwork),
		"traffic":     module.HandlerFor(m.traffic),
		"ping_node":   module.HandlerFor(m.ping),
	}
}

func (m *Module) report(ctx context.Context) (Report, error) {
	var info module.NodeInfo
	if err := m.env.Call(ctx, "node.info", nil, &info); err != nil {
		return Report{}, err
	}
	var state struct {
		Network json.RawMessage            `json:"network"`
		Modules map[string]json.RawMessage `json:"modules"`
	}
	if err := m.env.Call(ctx, "node.inspect", nil, &state); err != nil {
		return Report{}, err
	}
	r := Report{
		ID:       info.ID,
		Name:     info.Name,
		Addrs:    info.Addrs,
		Direct:   len(info.DirectAddrs) > 0,
		Observed: info.Observed,
		Network:  state.Network,
		Modules:  map[string]ModuleReport{},
	}
	for _, mod := range info.Modules {
		if mod.Runtime == "builtin" {
			continue
		}
		mr := ModuleReport{State: state.Modules[mod.Name]}
		for _, c := range mod.Capabilities {
			if !c.Internal {
				mr.Capabilities = append(mr.Capabilities, c.Ref)
			}
		}
		r.Modules[mod.Name] = mr
	}
	return r, nil
}

type mapped struct {
	Nodes []Report `json:"nodes"`
}

// mapNetwork reports on every node running the debug module.
func (m *Module) mapNetwork(ctx context.Context, _ struct{}) (mapped, error) {
	ctx = module.Untraced(ctx) // keep our own polling out of the traffic
	nodes, err := m.debugNodes(ctx)
	if err != nil {
		return mapped{}, err
	}
	reports := make([]Report, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() {
			var err error
			if n.ID == m.env.NodeID {
				reports[i], err = m.report(ctx)
			} else {
				err = m.env.CallNode(ctx, peerOf(n), Name+".report", nil, &reports[i])
			}
			if err != nil {
				reports[i] = Report{ID: n.ID, Name: n.Name, Addrs: n.Addrs, Error: err.Error()}
			}
		})
	}
	wg.Wait()
	slices.SortFunc(reports, func(a, b Report) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return mapped{Nodes: reports}, nil
}

// debugNodes finds the nodes running the debug module, as providers of
// debug.report. It reuses the answer for a few seconds, as tools poll.
func (m *Module) debugNodes(ctx context.Context) ([]node, error) {
	m.mu.Lock()
	nodes, fresh := m.nodes, time.Since(m.found) < 10*time.Second
	m.mu.Unlock()
	if fresh {
		return nodes, nil
	}
	var found struct {
		Providers []node `json:"providers"`
	}
	if err := m.env.Call(ctx, "discovery.find_capability_providers", map[string]string{"capability": Name + ".report"}, &found); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.nodes, m.found = found.Providers, time.Now()
	m.mu.Unlock()
	return found.Providers, nil
}

type pingOutput struct {
	RTT string `json:"rtt"`
}

// ping pings the node with the given ID, through a relay if needed. TLS
// proves it's the right node.
func (m *Module) ping(ctx context.Context, in struct {
	ID string `json:"id"`
}) (pingOutput, error) {
	var found node
	if err := m.env.Call(ctx, "discovery.find_node_by_id", in, &found); err != nil {
		return pingOutput{}, err
	}
	start := time.Now()
	if err := m.env.CallNode(ctx, peerOf(found), "network.ping", nil, nil); err != nil {
		return pingOutput{}, err
	}
	return pingOutput{RTT: time.Since(start).Round(time.Microsecond).String()}, nil
}

func peerOf(n node) module.Peer {
	return module.Peer{ID: n.ID, Addrs: n.Addrs}
}
