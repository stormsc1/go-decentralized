package node

import (
	"context"
	"fmt"
	"sync"

	"go-decentralized/internal/api"
	"go-decentralized/internal/module"
)

// Node is a running instance of a node definition with its modules loaded.
type Node struct {
	Config   Config
	Env      module.Env
	Registry *module.Registry
}

// New instantiates every module listed in cfg using the given factories.
func New(cfg Config, env module.Env, factories map[string]module.Factory) (*Node, error) {
	reg := module.NewRegistry()
	env.NodeName = cfg.Name
	env.Registry = reg
	for _, mc := range cfg.Modules {
		factory, ok := factories[mc.Name]
		if !ok {
			return nil, fmt.Errorf("node %q: unknown module %q", cfg.Name, mc.Name)
		}
		decode := func(v any) error {
			if mc.Config.IsZero() {
				return nil
			}
			return mc.Config.Decode(v)
		}
		m, err := factory(decode, env)
		if err != nil {
			return nil, fmt.Errorf("node %q: module %q: %w", cfg.Name, mc.Name, err)
		}
		if err := reg.Register(m); err != nil {
			return nil, err
		}
	}
	return &Node{Config: cfg, Env: env, Registry: reg}, nil
}

// Run runs the background work of every module until ctx is done.
func (n *Node) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, m := range n.Registry.Modules() {
		if r, ok := m.(module.Runner); ok {
			wg.Go(func() { r.Run(ctx) })
		}
	}
	wg.Wait()
}

// Info describes this node for other peers.
func (n *Node) Info() api.NodeInfo {
	info := api.NodeInfo{
		ID:          n.Env.NodeID,
		Name:        n.Config.Name,
		Description: n.Config.Desc,
		Addrs:       n.Env.Network.Addrs(),
	}
	for _, m := range n.Registry.Modules() {
		mi := api.ModuleInfo{Name: m.Name()}
		for _, c := range m.Capabilities() {
			mi.Capabilities = append(mi.Capabilities, api.CapabilityInfo{Name: c.Name(), Description: c.Description()})
		}
		info.Modules = append(info.Modules, mi)
	}
	return info
}
