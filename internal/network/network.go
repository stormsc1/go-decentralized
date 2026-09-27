// Package network is a node's transport. It carries calls between nodes as
// messages over WebSockets, encrypted end to end (noise.go), directly or
// through relays, and works out the addresses the node can be reached at.
// Modules never see it: the node dispatches the calls it receives, and makes
// the calls modules ask for. See spec/wire.md.
package network

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"maps"
	"slices"
	"sync"

	"go-decentralized/internal/noise"
	"go-decentralized/module"
)

// Handler handles a call from another node to the capability ref, with body
// as its input. ctx tells who made the call, see RemoteID.
type Handler func(ctx context.Context, ref string, body json.RawMessage) (json.RawMessage, error)

type Config struct {
	// Key is the node's key: its ID, which sessions prove.
	Key ed25519.PrivateKey
	// ListenPort is the port this node accepts connections on, 0 if none.
	ListenPort int
	// Plaintext makes the node serve plain HTTP instead of dressing its
	// listener in self-signed TLS: for platforms that end TLS in front of
	// it, such as Cloud Run. Sessions stay encrypted end to end either way.
	Plaintext bool
	// Announce are addresses always advertised, e.g. a public URL.
	Announce []string
	// Private lets addresses that only nearby peers can reach (private,
	// loopback, ...) count as reachable, for networks without public
	// addresses such as local development.
	Private bool
	Relay   RelayConfig
}

// Network is a node's transport.
type Network struct {
	cfg      Config
	id       string
	cert     tls.Certificate  // dressing for the listener, see serve
	static   *ecdh.PrivateKey // the sessions' Noise key
	identity []byte           // ties static to the node's key, see noise.go

	mu           sync.Mutex
	handler      Handler
	sessions     map[string][]*session // open sessions, by peer ID
	dialing      map[string]*dialing   // sessions being dialed, by peer
	reachable    []string              // direct addresses confirmed by dial-back
	observed     []string              // public IPs peers saw this node at
	relayed      []string              // addresses through relays holding a reservation for us
	reservations map[string]*secured   // held here, as a relay, by node ID
	waiting      map[string]*waiting   // callers' connections, until their node takes them
	traces       []Trace               // what this node sent, oldest first
}

//go:embed network.module.yaml
var manifest []byte

func New(cfg Config) (*Network, error) {
	cert, err := certificate(cfg.Key)
	if err != nil {
		return nil, err
	}
	static, err := noise.NewStatic()
	if err != nil {
		return nil, err
	}
	sig, err := module.Sign(cfg.Key, noisePurpose, static.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(identity{Key: cfg.Key.Public().(ed25519.PublicKey), Sig: sig})
	if err != nil {
		return nil, err
	}
	empty := func(s string) bool { return s == "" } // unset ${VAR}s
	cfg.Announce = slices.DeleteFunc(cfg.Announce, empty)
	cfg.Relay.Via = slices.DeleteFunc(cfg.Relay.Via, empty)
	n := &Network{
		cfg:          cfg,
		id:           module.NodeID(cfg.Key.Public().(ed25519.PublicKey)),
		cert:         cert,
		static:       static,
		identity:     payload,
		sessions:     map[string][]*session{},
		dialing:      map[string]*dialing{},
		reservations: map[string]*secured{},
		waiting:      map[string]*waiting{},
	}
	return n, nil
}

// Manifest describes the network's own capabilities, which the node serves
// like a module's.
func (n *Network) Manifest() module.Manifest { return module.MustParseManifest(manifest) }

// Handlers handle the network's own capabilities.
func (n *Network) Handlers() map[string]module.Handler {
	return map[string]module.Handler{
		"ping":      module.HandlerFor(n.ping),
		"dial_back": module.HandlerFor(n.dialBack),
	}
}

// Connected reports whether this node has a session with the node id.
func (n *Network) Connected(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.sessions[id]) > 0
}

// SetHandler sets the handler for the calls other nodes make.
func (n *Network) SetHandler(h Handler) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.handler = h
}

// Peer is a node to call: its ID, which the node answering must prove if
// set, and the addresses to try, in order.
type Peer struct {
	ID    string   `json:"id,omitempty"`
	Addrs []string `json:"addrs,omitempty"`
}

// Run keeps the node's addresses up to date until ctx is done: it asks peers
// which ones they can reach, and keeps its relay reservations. Then it
// closes its sessions. peers returns nodes that accept connections, outside
// any NAT this node is behind, to ask for dial-backs.
func (n *Network) Run(ctx context.Context, peers func() []Peer) {
	var wg sync.WaitGroup
	wg.Go(func() { n.watchReachability(ctx, peers) })
	for _, relay := range n.cfg.Relay.Via {
		wg.Go(func() { n.keepReservation(ctx, relay) })
	}
	wg.Wait()
	n.closeSessions()
}

// Addrs returns every address this node can be reached at: direct ones
// first, then through relays.
func (n *Network) Addrs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Concat(n.cfg.Announce, n.reachable, n.relayed)
}

// DirectAddrs returns the addresses other nodes can connect to directly.
// Nodes without any run in client mode.
func (n *Network) DirectAddrs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Concat(n.cfg.Announce, n.reachable)
}

// Observed returns the public IPs other nodes see this node's connections
// come from. Nodes behind the same NAT share them.
func (n *Network) Observed() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.observed)
}

func (n *Network) ListenPort() int { return n.cfg.ListenPort }

// Status reports the network's state, for debugging.
type Status struct {
	Reachable []string `json:"reachable,omitempty"`
	RelayVia  []string `json:"relay_via,omitempty"`
	// Sessions are the IDs of the peers the node has a session with.
	Sessions []string `json:"sessions,omitempty"`
	// Reservations are the IDs of the nodes holding a reservation here.
	Reservations []string `json:"reservations,omitempty"`
}

func (n *Network) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	return Status{n.reachable, n.cfg.Relay.Via, slices.Sorted(maps.Keys(n.sessions)), slices.Sorted(maps.Keys(n.reservations))}
}

// find looks key up in one of n's maps.
func find[V any](n *Network, m map[string]V, key string) V {
	n.mu.Lock()
	defer n.mu.Unlock()
	return m[key]
}
