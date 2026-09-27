package node

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"

	"go-decentralized/internal/api"
	"go-decentralized/internal/identity"
	"go-decentralized/internal/module"
	"go-decentralized/internal/network"
)

// Node is a running instance of a node definition: its modules, and the
// network that carries their messages.
type Node struct {
	Config   Config
	Env      module.Env
	Registry *module.Registry
	Network  *network.Network
}

// New instantiates every module listed in cfg using the given factories. The
// modules send messages through nw, and the messages they handle are routed
// to them.
func New(cfg Config, key ed25519.PrivateKey, nw *network.Network, factories map[string]module.Factory) (*Node, error) {
	reg := module.NewRegistry()
	env := module.Env{
		NodeID:   identity.NodeID(key),
		Key:      key,
		NodeName: cfg.Name,
		Send:     send(nw),
		Network:  nw,
		Registry: reg,
	}
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
		if r, ok := m.(module.Receiver); ok {
			for name, h := range r.Messages() {
				nw.Handle(m.Name()+"."+name, receive(h))
			}
		}
	}
	return &Node{Config: cfg, Env: env, Registry: reg, Network: nw}, nil
}

// send gives modules the network's Send, keeping untraced contexts untraced.
func send(nw *network.Network) module.SendFunc {
	return func(ctx context.Context, to api.Peer, name string, req, resp any) error {
		if module.IsUntraced(ctx) {
			ctx = network.WithoutTrace(ctx)
		}
		return nw.Send(ctx, to, name, req, resp)
	}
}

// receive adapts a module's handler to the network, telling it who sent the
// message.
func receive(h module.Handler) network.Handler {
	return func(ctx context.Context, decode func(any) error) (any, error) {
		return h(module.WithSender(ctx, network.RemoteID(ctx)), decode)
	}
}

// Run runs the network and the background work of every module until ctx is
// done.
func (n *Node) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { n.Network.Run(ctx, n.peers) })
	for _, m := range n.Registry.Modules() {
		if r, ok := m.(module.Runner); ok {
			wg.Go(func() { r.Run(ctx) })
		}
	}
	wg.Wait()
}

// peers collects the nodes the modules know, for the network to check the
// node's reachability with.
func (n *Node) peers(ctx context.Context) []api.Peer {
	var peers []api.Peer
	for _, m := range n.Registry.Modules() {
		if s, ok := m.(module.PeerSource); ok {
			peers = append(peers, s.Peers(ctx)...)
		}
	}
	return peers
}

// Info describes this node for the local API.
func (n *Node) Info() api.NodeInfo {
	info := api.NodeInfo{
		ID:          n.Env.NodeID,
		Name:        n.Config.Name,
		Description: n.Config.Desc,
		Addrs:       n.Network.Addrs(),
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
