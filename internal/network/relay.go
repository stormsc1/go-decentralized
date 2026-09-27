package network

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Nodes that accept no connections, e.g. behind a NAT, stay reachable
// through a relay: a node configured to serve as one. Such a node keeps a
// reservation on the relay: a WebSocket it opened itself, secured like a
// session, over which the relay names each caller's connection. The node
// takes a connection at acceptPath, and the relay then forwards WebSocket
// messages between it and the caller, who runs the session's handshake over
// them, end to end: the relay only sees ciphertext. The node advertises
// <relay address>/v1/relay/<its ID>, which callers dial like any address.

const takeTimeout = 10 * time.Second // for a node to take a caller's connection

// RelayConfig is the network.relay block of a node definition.
type RelayConfig struct {
	// Serve makes this node a relay for nodes that accept no connections.
	Serve bool `yaml:"serve"`
	// Via are the addresses of relays to keep a reservation on, making this
	// node reachable through them.
	Via []string `yaml:"via"`
}

// notice is what a relay tells a node over its reservation: that it holds
// the reservation, or that a caller's connection waits.
type notice struct {
	Reserved   bool   `json:"reserved,omitempty"`
	Connection string `json:"connection,omitempty"`
}

// waiting is a caller's connection waiting for its node to take it.
type waiting struct {
	taken chan *websocket.Conn // the node's end, once it takes it
	done  chan struct{}        // closed once the relay is done with it
}

// isRelay reports whether addr is through a relay.
func isRelay(addr string) bool {
	_, _, ok := relayAddr(addr)
	return ok
}

// relayAddr splits a relayed address, <relay address>/v1/relay/<node ID>,
// into the relay's own address and the target's ID.
func relayAddr(addr string) (relay, target string, ok bool) {
	i := strings.Index(addr, relayPrefix)
	if i < 0 {
		return "", "", false
	}
	return addr[:i], addr[i+len(relayPrefix):], true
}

// keepReservation holds a reservation on the relay at relay until ctx is
// done, reconnecting when it drops.
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

// holdReservation opens a reservation on relay, advertises the relayed
// address and takes the connections callers make through it, until the
// reservation closes. The sessions it took stay open until ctx is done.
func (n *Network) holdReservation(ctx context.Context, relay string) error {
	// The reservation is secured like a session: it proves this node's ID to
	// the relay, and the relay's notices arrive encrypted.
	start := time.Now()
	ws, err := n.dialWS(ctx, relay+reservePath)
	var enc *secured
	if err == nil {
		if _, enc, err = n.secure(ctx, ws, true, ""); err != nil {
			ws.CloseNow()
		}
	}
	n.trace(ctx, "stream", "relay.reserve", relay, "", start, err)
	if err != nil {
		return err
	}
	held, stop := context.WithCancel(ctx)
	defer stop()
	defer enc.Close()
	go keepAlive(held, ws)

	// The relay confirms once it holds the reservation, so the address is
	// only advertised once callers can use it.
	var first notice
	if data, err := enc.Read(); err != nil {
		return err
	} else if json.Unmarshal(data, &first) != nil || !first.Reserved {
		return errors.New("the relay didn't confirm the reservation")
	}
	addr := relay + relayPrefix + n.id
	n.mu.Lock()
	n.relayed = append(n.relayed, addr)
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		n.relayed = slices.DeleteFunc(n.relayed, func(a string) bool { return a == addr })
		n.mu.Unlock()
	}()
	slog.Info("relay: reachable through relay", "addr", addr)

	for {
		data, err := enc.Read()
		if err != nil {
			return err
		}
		var in notice
		if err := json.Unmarshal(data, &in); err != nil {
			return err
		}
		if in.Connection != "" {
			go n.takeConnection(ctx, relay, in.Connection)
		}
	}
}

// takeConnection takes a caller's connection from the relay, and answers the
// session the caller opens over it.
func (n *Network) takeConnection(ctx context.Context, relay, connection string) {
	start := time.Now()
	ws, err := n.dialWS(ctx, relay+acceptPath+"?connection="+url.QueryEscape(connection))
	var peer string
	var sc *secured
	if err == nil {
		if peer, sc, err = n.secure(ctx, ws, false, ""); err != nil {
			ws.CloseNow()
		}
	}
	n.trace(ctx, "stream", "relay.accept", relay, peer, start, err)
	if err != nil {
		slog.Debug("relay: can't take a connection", "relay", relay, "err", err)
		return
	}
	n.startSession(sc, peer, relay+relayPrefix, false, true)
}

// keepAlive pings ws until ctx is done, and closes it if the other end stops
// answering. The pings also keep NAT mappings open.
func keepAlive(ctx context.Context, ws *websocket.Conn) {
	t := time.NewTicker(keepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, keepEvery/3)
		err := ws.Ping(pctx)
		cancel()
		if err != nil && ctx.Err() == nil {
			ws.CloseNow()
			return
		}
	}
}

// serveReserve holds a reservation for the node at the other end, by the ID
// it proves, until it closes.
func (n *Network) serveReserve(w http.ResponseWriter, r *http.Request) {
	id, enc, err := n.accept(w, r)
	if err != nil {
		return
	}
	n.mu.Lock()
	if old := n.reservations[id]; old != nil {
		old.Close()
	}
	n.reservations[id] = enc
	n.mu.Unlock()
	if confirmation, err := json.Marshal(notice{Reserved: true}); err == nil {
		if err := enc.Write(confirmation); err != nil {
			enc.Close() // the loop below then ends, and cleans up
		}
	}

	slog.Info("relay: reservation opened", "node", id)
	// The node never sends over its reservation, so this blocks until it
	// closes, answering its pings.
	for {
		if _, err := enc.Read(); err != nil {
			break
		}
	}
	slog.Info("relay: reservation closed", "node", id)

	n.mu.Lock()
	if n.reservations[id] == enc {
		delete(n.reservations, id)
	}
	n.mu.Unlock()
}

// serveConnect tells the node a caller asks for about the caller's
// connection, over its reservation, and forwards between the two once the
// node takes it.
func (n *Network) serveConnect(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("id")
	reservation := find(n, n.reservations, node)
	if reservation == nil {
		http.Error(w, "no reservation for that node", http.StatusNotFound)
		return
	}
	name := rand.Text()
	c := &waiting{taken: make(chan *websocket.Conn), done: make(chan struct{})}
	n.mu.Lock()
	n.waiting[name] = c
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.waiting, name)
		n.mu.Unlock()
		close(c.done)
	}()

	incoming, err := json.Marshal(notice{Connection: name})
	if err == nil {
		err = reservation.Write(incoming)
	}
	if err != nil {
		http.Error(w, "the node's reservation is gone", http.StatusServiceUnavailable)
		return
	}
	var taken *websocket.Conn
	select {
	case taken = <-c.taken:
	case <-time.After(takeTimeout):
		http.Error(w, "the node didn't take the connection", http.StatusGatewayTimeout)
		return
	case <-r.Context().Done():
		return
	}
	// The caller speaks the session's subprotocol with the relay: the real
	// negotiation is the handshake it runs with the node.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{subprotocol}})
	if err != nil {
		taken.CloseNow()
		return
	}
	forward(ws, taken)
}

// serveAccept hands the node at the other end the caller's connection it was
// told about. The connection's name is an unguessable secret that only
// reached the node, over its reservation.
func (n *Network) serveAccept(w http.ResponseWriter, r *http.Request) {
	c := find(n, n.waiting, r.URL.Query().Get("connection"))
	if c == nil {
		http.Error(w, "no such connection", http.StatusNotFound)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{subprotocol}})
	if err != nil {
		return
	}
	select {
	case c.taken <- ws:
		<-c.done
	case <-c.done:
		ws.CloseNow()
	}
}

// forward carries WebSocket messages between a and b, as they are, until
// either side is done, then closes both.
func forward(a, b *websocket.Conn) {
	ctx := context.Background()
	done := make(chan struct{}, 2)
	copyTo := func(dst, src *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			kind, data, err := src.Read(ctx)
			if err != nil {
				return
			}
			if dst.Write(ctx, kind, data) != nil {
				return
			}
		}
	}
	go copyTo(a, b)
	go copyTo(b, a)
	<-done
	a.CloseNow()
	b.CloseNow()
}
