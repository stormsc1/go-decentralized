// Package network carries messages and streams between nodes. Modules define
// messages and exchange them through a Messenger without knowing how they
// travel: transports such as relays plug in underneath as dialers.
package network

import (
	"context"
	"net"
	"slices"
	"sync"
)

// Handler handles one kind of message. decode unmarshals the request into v.
type Handler func(ctx context.Context, decode func(v any) error) (any, error)

// Messenger exchanges request/response messages with other nodes.
type Messenger interface {
	// Send delivers req to the node reachable at any of addrs, tried in
	// order, and decodes its reply into resp.
	Send(ctx context.Context, addrs []string, name string, req, resp any) error
	// Handle registers the handler for messages called name.
	Handle(name string, h Handler)
}

// Handle registers a typed handler for messages called name.
func Handle[Req, Resp any](m Messenger, name string, h func(context.Context, Req) (Resp, error)) {
	m.Handle(name, func(ctx context.Context, decode func(any) error) (any, error) {
		var req Req
		if err := decode(&req); err != nil {
			return nil, err
		}
		return h(ctx, req)
	})
}

// streamHandler checks a stream request, then rejects it with an error or
// returns the function that takes over the connection.
type streamHandler func(ctx context.Context, decode func(v any) error) (func(net.Conn), error)

// HandleStream registers a typed handler for streams called name: raw
// connections opened with OpenStream. h checks the request, then rejects it
// with an error or returns the function that takes over the connection.
func HandleStream[Req any](n *Network, name string, h func(context.Context, Req) (func(net.Conn), error)) {
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

// DialFunc connects to an address of a transport, see RegisterDialer.
type DialFunc func(ctx context.Context, addr string) (net.Conn, error)

type Config struct {
	// ID is this node's ID, used to verify dial-backs reached this node.
	ID string
	// ListenPort is the port this node accepts connections on, 0 if none.
	ListenPort int
	// Announce are addresses always advertised, e.g. a public DNS name.
	Announce []string
	// Private lets addresses that only nearby peers can reach (private,
	// loopback, ...) count as reachable, for networks without public
	// addresses such as local development.
	Private bool
}

// Network is the Messenger of a node. It also knows the addresses the node
// can be reached at.
type Network struct {
	cfg       Config
	mu        sync.Mutex
	handlers  map[string]Handler
	streams   map[string]streamHandler
	dialers   map[string]DialFunc
	tracers   []func(Trace)
	reachable []string // direct addresses confirmed by dial-back
	observed  []string // IPs peers saw this node at, from dial-backs
	indirect  []string // addresses provided by transports, e.g. relays
}

func New(cfg Config) *Network {
	cfg.Announce = slices.DeleteFunc(cfg.Announce, func(s string) bool { return s == "" })
	n := &Network{
		cfg:      cfg,
		handlers: map[string]Handler{},
		streams:  map[string]streamHandler{},
		dialers:  map[string]DialFunc{},
	}
	Handle(n, msgPing, n.ping)
	Handle(n, msgDialBack, n.dialBack)
	return n
}

// Addrs returns every address this node can be reached at: direct ones
// first, then indirect ones such as relay addresses.
func (n *Network) Addrs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Concat(n.cfg.Announce, n.reachable, n.indirect)
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

// AddAddr advertises an indirect address, e.g. one through a relay, until
// RemoveAddr is called.
func (n *Network) AddAddr(addr string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.indirect = append(n.indirect, addr)
}

func (n *Network) RemoveAddr(addr string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.indirect = slices.DeleteFunc(n.indirect, func(a string) bool { return a == addr })
}

// RegisterDialer makes addresses of the form "<scheme>/<rest>" reachable
// through d, which is called with <rest>.
func (n *Network) RegisterDialer(scheme string, d DialFunc) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.dialers[scheme] = d
}

func (n *Network) ListenPort() int { return n.cfg.ListenPort }

func (n *Network) Handle(name string, h Handler) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.handlers[name] = h
}

// find looks key up in one of n's maps.
func find[V any](n *Network, m map[string]V, key string) V {
	n.mu.Lock()
	defer n.mu.Unlock()
	return m[key]
}
