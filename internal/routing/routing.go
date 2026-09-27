// Package routing finds nodes, and the nodes that provide a capability,
// without a central registry: over a Kademlia DHT, and on the local network
// with mDNS. It keeps what a node knows of the others, and tells the node how
// to reach them. See spec/routing.md.
package routing

import (
	"context"
	"crypto/ed25519"
	_ "embed"
	"log/slog"
	"slices"
	"sync"
	"time"

	"go-decentralized/internal/network"
	"go-decentralized/internal/routing/kademlia"
	"go-decentralized/module"
)

// Name is the name of routing's capabilities: routing.<capability>.
const Name = "routing"

//go:embed routing.module.yaml
var manifest []byte

type Config struct {
	// Key is the node's key, which signs its record.
	Key  ed25519.PrivateKey
	Name string
	// Network carries the DHT's calls, and tells the node's addresses.
	Network *network.Network
	// Bootstrap are addresses of any nodes already in the network.
	Bootstrap []string
	// MDNS advertises this node and finds others on the local network.
	MDNS bool
	// Provides returns the capabilities this node announces.
	Provides func() []string
	// Memory keeps what the node learns across restarts, if it has somewhere
	// to: the peers it knew, to rejoin through.
	Memory Memory
}

// Memory keeps values across restarts, see Config.Memory.
type Memory interface {
	// Get decodes the value of key into v.
	Get(ctx context.Context, key string, v any) error
	Put(ctx context.Context, key string, v any) error
}

// peersKey is where routing remembers the peers it knew.
const peersKey = "peers"

type Routing struct {
	cfg Config
	id  kademlia.ID
	dht *kademlia.DHT

	mu  sync.Mutex
	lan map[kademlia.ID]kademlia.Contact // peers on the local network, see browseLAN
}

func New(cfg Config) (*Routing, error) {
	cfg.Bootstrap = slices.DeleteFunc(cfg.Bootstrap, func(s string) bool { return s == "" }) // unset ${VAR}s
	pub := cfg.Key.Public().(ed25519.PublicKey)
	id, err := kademlia.ParseID(module.NodeID(pub))
	if err != nil {
		return nil, err
	}
	r := &Routing{cfg: cfg, id: id}
	r.dht = kademlia.New(kademlia.Config{
		PublicKey: pub,
		Sign: func(_ context.Context, purpose string, data []byte) ([]byte, error) {
			return module.Sign(cfg.Key, purpose, data)
		},
		Name:        cfg.Name,
		Addrs:       cfg.Network.Addrs,
		DirectAddrs: cfg.Network.DirectAddrs,
		Call: func(ctx context.Context, to network.Peer, name string, in, out any) error {
			body, err := module.Encode(in)
			if err != nil {
				return err
			}
			result, err := cfg.Network.Call(ctx, to, Name+"."+name, body)
			if err != nil {
				return err
			}
			return module.Decode(result, out)
		},
		Bootstrap: r.bootstrap,
		Refresh:   time.Minute,
		Republish: 10 * time.Minute,
		Provides:  cfg.Provides,
		Changed:   r.remember,
	})
	return r, nil
}

// remember keeps the nodes in the routing table, to rejoin through next
// time.
func (r *Routing) remember(known []kademlia.Contact) {
	if r.cfg.Memory == nil {
		return
	}
	if err := r.cfg.Memory.Put(context.Background(), peersKey, known); err != nil {
		slog.Warn("routing: can't remember peers", "err", err)
	}
}

// remembered returns the peers remembered from before, if any.
func (r *Routing) remembered(ctx context.Context) []kademlia.Contact {
	if r.cfg.Memory == nil {
		return nil
	}
	var peers []kademlia.Contact
	if err := r.cfg.Memory.Get(ctx, peersKey, &peers); err != nil {
		return nil // none yet
	}
	return peers
}

// Manifest describes routing's capabilities, which the node serves like a
// module's.
func (r *Routing) Manifest() module.Manifest { return module.MustParseManifest(manifest) }

// Handlers handle routing's capabilities.
func (r *Routing) Handlers() map[string]module.Handler {
	hs := r.dht.Handlers()
	hs["list_nodes"] = module.HandlerFor(r.listNodes)
	hs["find_node"] = module.HandlerFor(r.findNode)
	hs["find_providers"] = module.HandlerFor(r.findProviders)
	return hs
}

type nodes struct {
	Nodes []kademlia.Contact `json:"nodes"`
}

func (r *Routing) listNodes(ctx context.Context, _ struct{}) (nodes, error) {
	found, err := r.dht.Nodes(ctx)
	if err != nil {
		return nodes{}, module.Errorf(module.CodeUnavailable, "%v", err)
	}
	return nodes{Nodes: found}, nil
}

func (r *Routing) findNode(ctx context.Context, in struct {
	ID kademlia.ID `json:"id"`
}) (kademlia.Contact, error) {
	return r.find(ctx, in.ID)
}

type providers struct {
	Providers []kademlia.Contact `json:"providers"`
}

func (r *Routing) findProviders(ctx context.Context, in struct {
	Capability string `json:"capability"`
}) (providers, error) {
	found, err := r.dht.FindProviders(ctx, in.Capability)
	if err != nil {
		return providers{}, module.Errorf(module.CodeUnavailable, "%v", err)
	}
	return providers{Providers: found}, nil
}

// Resolve returns how to reach the node id, see find.
func (r *Routing) Resolve(ctx context.Context, id string) (network.Peer, error) {
	kid, err := kademlia.ParseID(id)
	if err != nil {
		return network.Peer{}, module.Errorf(module.CodeInvalidArgument, "%v", err)
	}
	c, err := r.find(ctx, kid)
	return network.Peer{ID: id, Addrs: c.Addrs}, err
}

// find returns what this node knows of the node id, looking it up in the
// DHT if it knows nothing. A peer on the local network is tried at its LAN
// address first, then at the addresses it advertises.
func (r *Routing) find(ctx context.Context, id kademlia.ID) (kademlia.Contact, error) {
	c, ok := r.dht.Local(id)
	if !ok {
		var err error
		if c, ok, err = r.dht.FindNode(ctx, id); err != nil {
			return c, module.Errorf(module.CodeUnavailable, "%v", err)
		}
	}
	lan, onLAN := r.lanPeer(id)
	switch {
	case onLAN && ok:
		others := slices.DeleteFunc(slices.Clone(c.Addrs), func(a string) bool { return slices.Contains(lan.Addrs, a) })
		c.Addrs = slices.Concat(lan.Addrs, others)
	case onLAN:
		c = lan
	case !ok:
		return c, module.Errorf(module.CodeNotFound, "node %s not found", id)
	}
	return c, nil
}

// Peers returns nodes to ask for dial-backs: those in the routing table,
// which accept connections and so are outside any NAT this node is behind,
// or the bootstrap nodes until it fills.
func (r *Routing) Peers() []network.Peer {
	var peers []network.Peer
	for _, c := range r.dht.Known() {
		peers = append(peers, network.Peer{ID: c.ID.String(), Addrs: c.Addrs})
	}
	if len(peers) > 0 {
		return peers
	}
	for _, addr := range r.cfg.Bootstrap {
		peers = append(peers, network.Peer{Addrs: []string{addr}})
	}
	for _, c := range r.lanPeers() {
		peers = append(peers, network.Peer{ID: c.ID.String(), Addrs: c.Addrs})
	}
	return peers
}

// Inspect reports the routing table and LAN peers, for debugging.
func (r *Routing) Inspect() any {
	return struct {
		RoutingTable []kademlia.Contact `json:"routing_table"`
		LANPeers     []kademlia.Contact `json:"lan_peers"`
	}{r.dht.Known(), r.lanPeers()}
}

// Run keeps the node in the DHT, and on the local network, until ctx is
// done.
func (r *Routing) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { r.dht.Run(ctx) })
	if r.cfg.MDNS {
		if port := r.cfg.Network.ListenPort(); port != 0 {
			wg.Go(func() { r.advertise(ctx, port) })
		}
		wg.Go(func() { r.watchLAN(ctx) })
	}
	wg.Wait()
}

// bootstrap returns the configured bootstrap nodes, the peers remembered
// from before, and any found on the local network.
func (r *Routing) bootstrap(ctx context.Context) []string {
	addrs := slices.Clone(r.cfg.Bootstrap)
	for _, c := range r.remembered(ctx) {
		addrs = append(addrs, c.Addrs...)
	}
	if r.cfg.MDNS {
		for _, c := range r.browseLAN(ctx) {
			addrs = append(addrs, c.Addrs...)
		}
	}
	return addrs
}
