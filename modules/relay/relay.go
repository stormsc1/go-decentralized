// Package relay makes nodes that accept no connections, such as nodes behind
// NAT or a firewall, reachable through a relay node.
//
// Such a node keeps a reservation on a relay: a connection it opened itself,
// multiplexed with yamux. It advertises relay/<relay host:port>/<node id>,
// and callers reach it by asking the relay to bridge them onto a new stream
// over the reservation. Relayed traffic isn't encrypted yet, see TODO.md.
package relay

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"go-decentralized/internal/module"
	"go-decentralized/internal/network"
	"go-decentralized/modules/relay/capabilities"
)

const Name = "relay"

// scheme starts relay addresses: relay/<relay host:port>/<node id>.
const scheme = "relay"

// Streams between nodes and a relay. Both name a node: the one making the
// reservation, or the one to connect to.
const (
	streamReserve = "relay.reserve"
	streamConnect = "relay.connect"
)

type request struct {
	ID string `json:"id"`
}

// Config is the `config:` block of the relay module in a node definition.
type Config struct {
	// Serve makes this node a relay for other nodes.
	Serve bool `yaml:"serve"`
	// Via are relays (host:port) to keep a reservation on, making this node
	// reachable through them.
	Via []string `yaml:"via"`
}

type Module struct {
	cfg Config
	id  string
	net *network.Network

	mu       sync.Mutex
	sessions map[string]*yamux.Session // reservations on this relay, by node ID
}

func New(decode func(any) error, env module.Env) (module.Module, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, err
	}
	cfg.Via = slices.DeleteFunc(cfg.Via, func(s string) bool { return s == "" }) // unset ${VAR}s
	m := &Module{cfg: cfg, id: env.NodeID, net: env.Network, sessions: map[string]*yamux.Session{}}
	env.Network.RegisterDialer(scheme, m.dial)
	if cfg.Serve {
		network.HandleStream(env.Network, streamReserve, m.handleReserve)
		network.HandleStream(env.Network, streamConnect, m.handleConnect)
	}
	return m, nil
}

func (m *Module) Name() string { return Name }

func (m *Module) Capabilities() []module.Capability {
	return []module.Capability{&capabilities.ListReservations{Reservations: m.reservations}}
}

// Inspect reports the relays this node reserves on and, on a relay, the
// reservations it holds, for the debug module.
func (m *Module) Inspect() any {
	return struct {
		Via          []string `json:"via,omitempty"`
		Reservations []string `json:"reservations,omitempty"`
	}{m.cfg.Via, m.reservations()}
}

// Run keeps this node's reservations until ctx is done.
func (m *Module) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, relay := range m.cfg.Via {
		wg.Go(func() { m.keepReservation(ctx, relay) })
	}
	wg.Wait()
}

// dial connects to a node through a relay. addr is
// "<relay host:port>/<node id>".
func (m *Module) dial(ctx context.Context, addr string) (net.Conn, error) {
	relay, id, ok := strings.Cut(addr, "/")
	if !ok {
		return nil, fmt.Errorf("invalid relay address %q", addr)
	}
	return m.net.OpenStream(ctx, []string{relay}, streamConnect, request{ID: id})
}

// keepReservation holds a reservation on relay until ctx is done,
// reconnecting when it drops.
func (m *Module) keepReservation(ctx context.Context, relay string) {
	for {
		err := m.holdReservation(ctx, relay)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("relay: reservation lost", "relay", relay, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// holdReservation opens a reservation on relay, advertises the relay address
// and serves what arrives through it until the reservation closes.
func (m *Module) holdReservation(ctx context.Context, relay string) error {
	conn, err := m.net.OpenStream(ctx, []string{relay}, streamReserve, request{ID: m.id})
	if err != nil {
		return err
	}
	session, err := yamux.Server(conn, nil)
	if err != nil {
		conn.Close()
		return err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()

	addr := scheme + "/" + relay + "/" + m.id
	m.net.AddAddr(addr)
	defer m.net.RemoveAddr(addr)
	slog.Info("relay: reachable through relay", "addr", addr)
	return m.net.Serve(session)
}
