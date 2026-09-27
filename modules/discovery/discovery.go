// Package discovery lets a node find the other nodes in the network through
// a Kademlia DHT, without any central registry.
package discovery

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"go-decentralized/internal/module"
	"go-decentralized/internal/network"
	"go-decentralized/modules/discovery/capabilities"
	"go-decentralized/modules/discovery/kademlia"
)

const Name = "discovery"

// Config is the `config:` block of the discovery module in a node definition.
type Config struct {
	// Bootstrap are addresses (host:port) of any nodes already in the network.
	Bootstrap []string `yaml:"bootstrap"`
	// MDNS advertises this node and finds bootstrap nodes on the local
	// network. Defaults to true.
	MDNS bool `yaml:"mdns"`
	// Refresh is how often the routing table is refreshed and capabilities
	// re-announced. Defaults to 1m.
	Refresh time.Duration `yaml:"refresh"`
}

type Module struct {
	cfg  Config
	id   kademlia.ID
	name string
	net  *network.Network
	dht  *kademlia.DHT

	mu  sync.Mutex
	lan map[kademlia.ID]kademlia.Contact // peers on the local network, see browseLAN
}

func New(decode func(any) error, env module.Env) (module.Module, error) {
	cfg := Config{MDNS: true, Refresh: time.Minute}
	if err := decode(&cfg); err != nil {
		return nil, err
	}
	cfg.Bootstrap = slices.DeleteFunc(cfg.Bootstrap, func(s string) bool { return s == "" }) // unset ${VAR}s
	id, err := kademlia.ParseID(env.NodeID)
	if err != nil {
		return nil, err
	}
	m := &Module{cfg: cfg, id: id, name: env.NodeName, net: env.Network}
	m.dht = kademlia.New(kademlia.Config{
		Key:         env.Key,
		Name:        env.NodeName,
		Addrs:       env.Network.Addrs,
		DirectAddrs: env.Network.DirectAddrs,
		Messenger:   env.Network,
		Bootstrap:   m.bootstrap,
		Refresh:     cfg.Refresh,
		Provides:    env.Registry.Refs,
	})
	return m, nil
}

func (m *Module) Name() string { return Name }

func (m *Module) Capabilities() []module.Capability {
	return []module.Capability{
		&capabilities.ListNodes{DHT: m.dht},
		&capabilities.FindNodeByID{Find: m.findNode},
		&capabilities.FindCapabilityProviders{DHT: m.dht},
	}
}

// Inspect reports the routing table and LAN peers, for the debug module.
func (m *Module) Inspect() any {
	return struct {
		RoutingTable []kademlia.Contact `json:"routing_table"`
		LANPeers     []kademlia.Contact `json:"lan_peers"`
	}{m.dht.Known(), m.lanPeers()}
}

func (m *Module) Run(ctx context.Context) {
	if port := m.net.ListenPort(); m.cfg.MDNS && port != 0 {
		stop, err := advertise(kademlia.Contact{ID: m.id, Name: m.name}, port)
		if err != nil {
			slog.Warn("discovery: mdns advertise failed", "err", err)
		} else {
			defer stop()
		}
	}
	var wg sync.WaitGroup
	wg.Go(func() { m.net.Run(ctx, m.peers) })
	wg.Go(func() { m.dht.Run(ctx) })
	if m.cfg.MDNS {
		wg.Go(func() { m.watchLAN(ctx) })
	}
	wg.Wait()
}

// bootstrap returns the configured bootstrap nodes plus any found via mDNS.
func (m *Module) bootstrap(ctx context.Context) []string {
	addrs := slices.Clone(m.cfg.Bootstrap)
	if m.cfg.MDNS {
		for _, c := range m.browseLAN(ctx) {
			addrs = append(addrs, c.Addrs...)
		}
	}
	return addrs
}

// peers returns addresses of nodes to ask for dial-backs: routing table
// contacts, which are servers outside any NAT and so see this node's public
// address. Until the table fills, e.g. because no node is known to be
// reachable yet, it falls back to the bootstrap nodes.
func (m *Module) peers(ctx context.Context) [][]string {
	var peers [][]string
	for _, c := range m.dht.Known() {
		peers = append(peers, c.Addrs)
	}
	if len(peers) > 0 {
		return peers
	}
	for _, addr := range m.bootstrap(ctx) {
		peers = append(peers, []string{addr})
	}
	return peers
}
