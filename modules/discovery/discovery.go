// Package discovery lets a node find the other nodes in the network through
// a Kademlia DHT, without any central registry.
package discovery

import (
	"context"
	_ "embed"
	"log/slog"
	"slices"
	"sync"
	"time"

	"go-decentralized/module"
	"go-decentralized/modules/discovery/kademlia"
)

const Name = "discovery"

//go:embed module.yaml
var manifest []byte

// Config is the `config:` block of the discovery module in a node definition.
type Config struct {
	// Bootstrap are addresses (host:port) of any nodes already in the network.
	Bootstrap []string `yaml:"bootstrap"`
	// MDNS advertises this node and finds bootstrap nodes on the local
	// network. Defaults to true.
	MDNS bool `yaml:"mdns"`
	// Refresh is how often the routing table is refreshed. Defaults to 1m.
	Refresh time.Duration `yaml:"refresh"`
	// Republish is how often this node's record and capabilities are
	// re-announced. Records live for 3x Republish. Defaults to 10m.
	Republish time.Duration `yaml:"republish"`
}

type Module struct {
	cfg Config
	env module.Env
	id  kademlia.ID
	dht *kademlia.DHT

	mu  sync.Mutex
	lan map[kademlia.ID]kademlia.Contact // peers on the local network, see browseLAN
}

func New(decode func(any) error, env module.Env) (module.Module, error) {
	cfg := Config{MDNS: true, Refresh: time.Minute, Republish: 10 * time.Minute}
	if err := decode(&cfg); err != nil {
		return nil, err
	}
	cfg.Bootstrap = slices.DeleteFunc(cfg.Bootstrap, func(s string) bool { return s == "" }) // unset ${VAR}s
	id, err := kademlia.ParseID(env.NodeID)
	if err != nil {
		return nil, err
	}
	m := &Module{cfg: cfg, env: env, id: id}
	m.dht = kademlia.New(kademlia.Config{
		PublicKey:   m.info().PublicKey,
		Sign:        env.Sign,
		Name:        env.NodeName,
		Addrs:       func() []string { return m.info().Addrs },
		DirectAddrs: func() []string { return m.info().DirectAddrs },
		Call: func(ctx context.Context, to module.Peer, name string, in, out any) error {
			return env.CallNode(ctx, to, Name+"."+name, in, out)
		},
		Bootstrap: m.bootstrap,
		Refresh:   cfg.Refresh,
		Republish: cfg.Republish,
		Provides:  m.provides,
	})
	return m, nil
}

func (m *Module) Manifest() module.Manifest { return module.MustParseManifest(manifest) }

func (m *Module) Handlers() map[string]module.Handler {
	hs := m.dht.Handlers()
	hs["list_nodes"] = module.HandlerFor(m.listNodes)
	hs["find_node_by_id"] = module.HandlerFor(m.findNodeByID)
	hs["find_capability_providers"] = module.HandlerFor(m.findProviders)
	return hs
}

type nodes struct {
	Nodes []kademlia.Contact `json:"nodes"`
}

func (m *Module) listNodes(ctx context.Context, _ struct{}) (nodes, error) {
	found, err := m.dht.Nodes(ctx)
	if err != nil {
		return nodes{}, module.Errorf(module.CodeUnavailable, "%v", err)
	}
	return nodes{Nodes: found}, nil
}

func (m *Module) findNodeByID(ctx context.Context, in struct {
	ID kademlia.ID `json:"id"`
}) (kademlia.Contact, error) {
	found, ok, err := m.findNode(ctx, in.ID)
	if err != nil {
		return found, module.Errorf(module.CodeUnavailable, "%v", err)
	}
	if !ok {
		return found, module.Errorf(module.CodeNotFound, "node %s not found", in.ID)
	}
	return found, nil
}

type providers struct {
	Providers []kademlia.Contact `json:"providers"`
}

func (m *Module) findProviders(ctx context.Context, in struct {
	Capability string `json:"capability"`
}) (providers, error) {
	found, err := m.dht.FindProviders(ctx, in.Capability)
	if err != nil {
		return providers{}, module.Errorf(module.CodeUnavailable, "%v", err)
	}
	return providers{Providers: found}, nil
}

// Inspect reports the routing table and LAN peers, for the debug module.
func (m *Module) Inspect() any {
	return struct {
		RoutingTable []kademlia.Contact `json:"routing_table"`
		LANPeers     []kademlia.Contact `json:"lan_peers"`
	}{m.dht.Known(), m.lanPeers()}
}

func (m *Module) Run(ctx context.Context) {
	if port := m.info().ListenPort; m.cfg.MDNS && port != 0 {
		stop, err := advertise(kademlia.Contact{ID: m.id, Name: m.env.NodeName}, port)
		if err != nil {
			slog.Warn("discovery: mdns advertise failed", "err", err)
		} else {
			defer stop()
		}
	}
	var wg sync.WaitGroup
	wg.Go(func() { m.dht.Run(ctx) })
	if m.cfg.MDNS {
		wg.Go(func() { m.watchLAN(ctx) })
	}
	wg.Wait()
}

// info describes this node: its key, addresses and modules. It's empty if
// the node doesn't answer.
func (m *Module) info() module.NodeInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var info module.NodeInfo
	if err := m.env.Call(ctx, "node.info", nil, &info); err != nil {
		slog.Warn("discovery: node.info failed", "err", err)
	}
	return info
}

// provides returns the capabilities this node announces: those other nodes
// may call, except the ones internal to a protocol.
func (m *Module) provides() []string {
	var refs []string
	for _, mod := range m.info().Modules {
		for _, c := range mod.Capabilities {
			if c.Access == module.Network && !c.Internal {
				refs = append(refs, c.Ref)
			}
		}
	}
	return refs
}

// bootstrap returns the configured bootstrap nodes plus any found via mDNS,
// other than this one.
func (m *Module) bootstrap(ctx context.Context) []string {
	addrs := slices.Clone(m.cfg.Bootstrap)
	if m.cfg.MDNS {
		for _, c := range m.browseLAN(ctx) {
			if c.ID != m.id {
				addrs = append(addrs, c.Addrs...)
			}
		}
	}
	return addrs
}
