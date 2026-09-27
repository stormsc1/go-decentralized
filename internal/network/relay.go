package network

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"go-decentralized/module"
)

// Nodes that accept no connections, e.g. behind a NAT, stay reachable
// through a relay. Such a node keeps a reservation on the relay: a WebSocket
// it opened itself, multiplexed with yamux, over which the relay bridges
// callers to it. It advertises relay/<relay host:port>/<node id>. TLS runs
// end to end between caller and node, so the relay only sees ciphertext,
// and a reservation is keyed by the ID its node proved.

const relayScheme = "relay"

// RelayConfig is the network.relay block of a node definition.
type RelayConfig struct {
	// Serve makes this node a relay for nodes that accept no connections.
	Serve bool `yaml:"serve"`
	// Via are relays (host:port) to keep a reservation on, making this node
	// reachable through them.
	Via []string `yaml:"via"`
}

// isRelay reports whether addr is through a relay.
func isRelay(addr string) bool {
	_, _, ok := relayAddr(addr)
	return ok
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
	ws, err := n.openRelay(ctx, relay, "relay.connect", connectPath+"?id="+url.QueryEscape(target))
	if err != nil {
		return nil, err
	}
	return websocket.NetConn(context.Background(), ws, websocket.MessageBinary), nil
}

// openRelay opens a WebSocket to the relay at relay, at path, tracing it as
// name.
func (n *Network) openRelay(ctx context.Context, relay, name, path string) (*websocket.Conn, error) {
	start := time.Now()
	ws, answered, err := n.connect(ctx, relay, "", path)
	n.trace(ctx, "stream", name, relay, answered, start, err)
	if err == nil {
		n.remember(module.Peer{ID: answered, Addrs: []string{relay}})
	}
	return ws, err
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
	ws, err := n.openRelay(ctx, relay, "relay.reserve", reservePath)
	if err != nil {
		return err
	}
	session, err := yamux.Server(websocket.NetConn(ctx, ws, websocket.MessageBinary), nil)
	if err != nil {
		ws.CloseNow()
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
	return n.serve(streams{session, relay})
}

// streams are the connections that reach this node through a relay: yamux
// streams over its reservation. Their remote address is the relay's path,
// "relay/<relay host:port>", and their deadline errors are temporary, like
// TCP's: HTTP servers hit a deadline on purpose when they hand a connection
// over, and TLS gives up on a connection after any error that isn't
// temporary.
type streams struct {
	net.Listener
	relay string
}

func (l streams) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	return stream{c, relayPath(relayScheme + "/" + l.relay)}, err
}

type stream struct {
	net.Conn
	remote net.Addr
}

func (c stream) RemoteAddr() net.Addr { return c.remote }

func (c stream) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		err = os.ErrDeadlineExceeded
	}
	return n, err
}

type relayPath string

func (relayPath) Network() string  { return relayScheme }
func (p relayPath) String() string { return string(p) }

// serveReserve holds a reservation for the node at the other end, by the ID
// it proved over TLS, until it closes.
func (n *Network) serveReserve(w http.ResponseWriter, r *http.Request) {
	id := peerNode(*r.TLS)
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	session, err := yamux.Client(websocket.NetConn(r.Context(), ws, websocket.MessageBinary), nil)
	if err != nil {
		ws.CloseNow()
		return
	}
	n.mu.Lock()
	if old := n.reservations[id]; old != nil {
		old.Close()
	}
	n.reservations[id] = session
	n.mu.Unlock()

	slog.Info("relay: reservation opened", "node", id)
	<-session.CloseChan()
	slog.Info("relay: reservation closed", "node", id)

	n.mu.Lock()
	if n.reservations[id] == session {
		delete(n.reservations, id)
	}
	n.mu.Unlock()
}

// serveConnect bridges a caller onto a new stream over the reservation of
// the node it asks for.
func (n *Network) serveConnect(w http.ResponseWriter, r *http.Request) {
	session := find(n, n.reservations, r.URL.Query().Get("id"))
	if session == nil {
		http.Error(w, "no reservation for that node", http.StatusNotFound)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	stream, err := session.Open()
	if err != nil {
		ws.CloseNow()
		return
	}
	bridge(websocket.NetConn(r.Context(), ws, websocket.MessageBinary), stream)
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
