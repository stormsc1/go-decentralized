package network

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/yamux"

	"go-decentralized/internal/api"
)

// Nodes that accept no connections, e.g. behind a NAT, stay reachable
// through a relay. Such a node keeps a reservation on the relay: a
// connection it opened itself, multiplexed with yamux, over which the relay
// bridges callers to it. It advertises relay/<relay host:port>/<node id>.
// TLS runs end to end between caller and node, so the relay only sees
// ciphertext, and a reservation is keyed by the ID its node proved.

const (
	relayScheme   = "relay"
	streamReserve = "relay.reserve"
	streamConnect = "relay.connect"
)

// RelayConfig is the network.relay block of a node definition.
type RelayConfig struct {
	// Serve makes this node a relay for nodes that accept no connections.
	Serve bool `yaml:"serve"`
	// Via are relays (host:port) to keep a reservation on, making this node
	// reachable through them.
	Via []string `yaml:"via"`
}

type connectRequest struct {
	ID string `json:"id"` // the node to connect to
}

// relayAddr splits a relay/<relay host:port>/<node id> address.
func relayAddr(addr string) (relay, target string, ok bool) {
	rest, ok := strings.CutPrefix(addr, relayScheme+"/")
	if !ok {
		return "", "", false
	}
	return strings.Cut(rest, "/")
}

// dialRelay connects to target through the relay at relay.
func (n *Network) dialRelay(ctx context.Context, relay, target string) (net.Conn, error) {
	return n.openStream(ctx, api.Peer{Addrs: []string{relay}}, streamConnect, connectRequest{ID: target})
}

// keepReservation holds a reservation on relay until ctx is done,
// reconnecting when it drops.
func (n *Network) keepReservation(ctx context.Context, relay string) {
	for {
		err := n.holdReservation(ctx, relay)
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
func (n *Network) holdReservation(ctx context.Context, relay string) error {
	conn, err := n.openStream(ctx, api.Peer{Addrs: []string{relay}}, streamReserve, struct{}{})
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

	addr := relayScheme + "/" + relay + "/" + n.id
	n.mu.Lock()
	n.relayed = append(n.relayed, addr)
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		n.relayed = slices.DeleteFunc(n.relayed, func(a string) bool { return a == addr })
		n.mu.Unlock()
	}()
	slog.Info("relay: reachable through relay", "addr", addr)
	return n.serve(session)
}

// handleReserve accepts a reservation from the node at the other end, as it
// proved over TLS, and holds it until it closes.
func (n *Network) handleReserve(ctx context.Context, _ struct{}) (func(net.Conn), error) {
	id := RemoteID(ctx)
	return func(conn net.Conn) {
		session, err := yamux.Client(conn, nil)
		if err != nil {
			conn.Close()
			return
		}
		n.mu.Lock()
		if old := n.sessions[id]; old != nil {
			old.Close()
		}
		n.sessions[id] = session
		n.mu.Unlock()

		slog.Info("relay: reservation opened", "node", id)
		<-session.CloseChan()
		slog.Info("relay: reservation closed", "node", id)

		n.mu.Lock()
		if n.sessions[id] == session {
			delete(n.sessions, id)
		}
		n.mu.Unlock()
	}, nil
}

// handleConnect bridges a caller onto a new stream over the reservation of
// the node it asks for.
func (n *Network) handleConnect(_ context.Context, req connectRequest) (func(net.Conn), error) {
	session := find(n, n.sessions, req.ID)
	if session == nil {
		return nil, fmt.Errorf("no reservation for %s", req.ID)
	}
	return func(conn net.Conn) {
		stream, err := session.Open()
		if err != nil {
			conn.Close()
			return
		}
		bridge(conn, stream)
	}, nil
}

// bridge copies between a and b until either side is done, then closes both.
func bridge(a, b net.Conn) {
	done := make(chan struct{}, 2)
	copyTo := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go copyTo(a, b)
	go copyTo(b, a)
	<-done
	a.Close()
	b.Close()
}
