// Package network is a node's transport. It carries messages between nodes
// over mutual TLS, directly or through relays, and works out the addresses
// the node can be reached at. Modules never see it: the node gives them a
// function to send messages and routes the messages they handle to them.
package network

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"maps"
	"net"
	"slices"
	"sync"

	"github.com/hashicorp/yamux"

	"go-decentralized/internal/api"
	"go-decentralized/internal/identity"
)

// Handler handles one kind of message. decode unmarshals the request into v.
type Handler func(ctx context.Context, decode func(v any) error) (any, error)

// streamHandler checks a stream request, then rejects it with an error or
// returns the function that takes over the connection.
type streamHandler func(ctx context.Context, decode func(v any) error) (func(net.Conn), error)

type Config struct {
	// Key is the node's key: its ID, and its identity in TLS.
	Key ed25519.PrivateKey
	// ListenPort is the port this node accepts connections on, 0 if none.
	ListenPort int
	// Announce are addresses always advertised, e.g. a public DNS name.
	Announce []string
	// Private lets addresses that only nearby peers can reach (private,
	// loopback, ...) count as reachable, for networks without public
	// addresses such as local development.
	Private bool
	Relay   RelayConfig
}

// Network is a node's transport.
type Network struct {
	cfg  Config
	id   string
	cert tls.Certificate

	mu        sync.Mutex
	handlers  map[string]Handler
	streams   map[string]streamHandler
	reachable []string                  // direct addresses confirmed by dial-back
	observed  []string                  // public IPs peers saw this node at
	relayed   []string                  // addresses through relays holding a reservation for us
	sessions  map[string]*yamux.Session // reservations held here, as a relay, by node ID
	traces    []api.Trace               // what this node sent, oldest first
}

func New(cfg Config) (*Network, error) {
	cert, err := certificate(cfg.Key)
	if err != nil {
		return nil, err
	}
	empty := func(s string) bool { return s == "" } // unset ${VAR}s
	cfg.Announce = slices.DeleteFunc(cfg.Announce, empty)
	cfg.Relay.Via = slices.DeleteFunc(cfg.Relay.Via, empty)
	n := &Network{
		cfg:      cfg,
		id:       identity.NodeID(cfg.Key),
		cert:     cert,
		handlers: map[string]Handler{},
		streams:  map[string]streamHandler{},
		sessions: map[string]*yamux.Session{},
	}
	handle(n, msgPing, n.ping)
	handle(n, msgDialBack, n.dialBack)
	if cfg.Relay.Serve {
		handleStream(n, streamReserve, n.handleReserve)
		handleStream(n, streamConnect, n.handleConnect)
	}
	return n, nil
}

// Run keeps the node's addresses up to date until ctx is done: it checks
// which ones peers can reach and keeps its relay reservations. peers returns
// known nodes to ask for dial-backs.
func (n *Network) Run(ctx context.Context, peers func(context.Context) []api.Peer) {
	var wg sync.WaitGroup
	wg.Go(func() { n.watchReachability(ctx, peers) })
	for _, relay := range n.cfg.Relay.Via {
		wg.Go(func() { n.keepReservation(ctx, relay) })
	}
	wg.Wait()
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

// Inspect reports the network's state, for debugging.
func (n *Network) Inspect() any {
	n.mu.Lock()
	defer n.mu.Unlock()
	return struct {
		Reachable    []string `json:"reachable,omitempty"`
		RelayVia     []string `json:"relay_via,omitempty"`
		Reservations []string `json:"reservations,omitempty"`
	}{n.reachable, n.cfg.Relay.Via, slices.Sorted(maps.Keys(n.sessions))}
}

// Handle registers the handler for messages called name.
func (n *Network) Handle(name string, h Handler) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.handlers[name] = h
}

// handle registers a typed handler for messages called name.
func handle[Req, Resp any](n *Network, name string, h func(context.Context, Req) (Resp, error)) {
	n.Handle(name, func(ctx context.Context, decode func(any) error) (any, error) {
		var req Req
		if err := decode(&req); err != nil {
			return nil, err
		}
		return h(ctx, req)
	})
}

// handleStream registers a typed handler for streams called name.
func handleStream[Req any](n *Network, name string, h func(context.Context, Req) (func(net.Conn), error)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.streams[name] = func(ctx context.Context, decode func(any) error) (func(net.Conn), error) {
		var req Req
		if err := decode(&req); err != nil {
			return nil, err
		}
		return h(ctx, req)
	}
}

// find looks key up in one of n's maps.
func find[V any](n *Network, m map[string]V, key string) V {
	n.mu.Lock()
	defer n.mu.Unlock()
	return m[key]
}
