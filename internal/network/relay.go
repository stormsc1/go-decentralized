package network

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Nodes that accept no connections, e.g. behind a NAT, stay reachable
// through a relay. Such a node keeps a reservation on the relay: a WebSocket
// it opened itself, over which the relay tells it about each caller. The node
// then opens a WebSocket to the relay to take the caller's connection, and
// the relay splices the two. TLS runs end to end between caller and node, so
// the relay only sees ciphertext, and a reservation is keyed by the ID its
// node proved. The node advertises relay/<relay host:port>/<node id>.

const (
	relayScheme = "relay"
	takeTimeout = 10 * time.Second // for a node to take a caller's connection
)

// RelayConfig is the network.relay block of a node definition.
type RelayConfig struct {
	// Serve makes this node a relay for nodes that accept no connections.
	Serve bool `yaml:"serve"`
	// Via are relays (host:port) to keep a reservation on, making this node
	// reachable through them.
	Via []string `yaml:"via"`
}

// incoming tells a node about a caller's connection, over its reservation.
type incoming struct {
	Connection string `json:"connection"`
}

// waiting is a caller's connection waiting for its node to take it.
type waiting struct {
	node  string        // the node it's for
	taken chan net.Conn // the node's end, once it takes it
	done  chan struct{} // closed once the relay is done with it
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
// and serves the connections callers make through it, until the reservation
// closes. The connections it took stay open until ctx is done.
func (n *Network) holdReservation(ctx context.Context, relay string) error {
	ws, err := n.openRelay(ctx, relay, "relay.reserve", reservePath)
	if err != nil {
		return err
	}
	held, stop := context.WithCancel(ctx)
	defer stop()
	defer ws.CloseNow()
	go keepAlive(held, ws)
	l := &relayed{conns: make(chan net.Conn), done: make(chan struct{}), addr: relayPath(relayScheme + "/" + relay)}
	defer l.Close()
	go n.serve(l)

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

	for {
		var in incoming
		if err := wsjson.Read(held, ws, &in); err != nil {
			return err
		}
		go func() {
			ws, err := n.openRelay(ctx, relay, "relay.accept", acceptPath+"?connection="+url.QueryEscape(in.Connection))
			if err != nil {
				slog.Debug("relay: can't take a connection", "relay", relay, "err", err)
				return
			}
			l.push(websocket.NetConn(ctx, ws, websocket.MessageBinary))
		}()
	}
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

// relayed are the connections callers make through a relay, for serve.
type relayed struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
	addr  relayPath
}

func (l *relayed) push(c net.Conn) {
	select {
	case l.conns <- newReadAhead(c, l.addr):
	case <-l.done:
		c.Close()
	}
}

func (l *relayed) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *relayed) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *relayed) Addr() net.Addr { return l.addr }

// relayPath is the remote address of connections through a relay:
// "relay/<relay host:port>".
type relayPath string

func (relayPath) Network() string  { return relayScheme }
func (p relayPath) String() string { return string(p) }

// readAhead reads a connection ahead, in a goroutine, so that reads time out
// without closing it, as HTTP servers need when they hand a connection over:
// websocket.NetConn closes a connection whose deadline passes. Writes have
// no deadline.
type readAhead struct {
	net.Conn
	remote net.Addr
	data   chan []byte   // what was read ahead
	err    error         // why reading stopped, once data is closed
	closed chan struct{} // closed with the connection
	once   sync.Once
	buf    []byte

	mu       sync.Mutex
	deadline time.Time
	moved    chan struct{} // closed when the deadline moves
}

func newReadAhead(c net.Conn, remote net.Addr) *readAhead {
	r := &readAhead{Conn: c, remote: remote, data: make(chan []byte), closed: make(chan struct{}), moved: make(chan struct{})}
	go func() {
		defer close(r.data)
		for {
			buf := make([]byte, 32<<10)
			n, err := c.Read(buf)
			if n > 0 {
				select {
				case r.data <- buf[:n]:
				case <-r.closed:
					return
				}
			}
			if err != nil {
				r.err = err
				return
			}
		}
	}()
	return r
}

func (r *readAhead) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		r.mu.Lock()
		deadline, moved := r.deadline, r.moved
		r.mu.Unlock()
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			wait := time.Until(deadline)
			if wait <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			t := time.NewTimer(wait)
			defer t.Stop()
			timeout = t.C
		}
		select {
		case b, ok := <-r.data:
			if !ok {
				if r.err == nil {
					return 0, io.EOF
				}
				return 0, r.err
			}
			r.buf = b
		case <-timeout:
			return 0, os.ErrDeadlineExceeded
		case <-moved:
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *readAhead) SetDeadline(t time.Time) error { return r.SetReadDeadline(t) }

func (r *readAhead) SetReadDeadline(t time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadline = t
	close(r.moved)
	r.moved = make(chan struct{})
	return nil
}

func (r *readAhead) SetWriteDeadline(time.Time) error { return nil }

func (r *readAhead) RemoteAddr() net.Addr { return r.remote }

func (r *readAhead) Close() error {
	r.once.Do(func() { close(r.closed) })
	return r.Conn.Close()
}

// serveReserve holds a reservation for the node at the other end, by the ID
// it proved over TLS, until it closes.
func (n *Network) serveReserve(w http.ResponseWriter, r *http.Request) {
	id := peerNode(*r.TLS)
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	n.mu.Lock()
	if old := n.reservations[id]; old != nil {
		old.CloseNow()
	}
	n.reservations[id] = ws
	n.mu.Unlock()

	slog.Info("relay: reservation opened", "node", id)
	<-ws.CloseRead(r.Context()).Done()
	slog.Info("relay: reservation closed", "node", id)

	n.mu.Lock()
	if n.reservations[id] == ws {
		delete(n.reservations, id)
	}
	n.mu.Unlock()
}

// serveConnect tells the node a caller asks for about the caller's
// connection, over its reservation, and splices it with the node's once it
// takes it.
func (n *Network) serveConnect(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("id")
	reservation := find(n, n.reservations, node)
	if reservation == nil {
		http.Error(w, "no reservation for that node", http.StatusNotFound)
		return
	}
	name := rand.Text()
	c := &waiting{node: node, taken: make(chan net.Conn), done: make(chan struct{})}
	n.mu.Lock()
	n.waiting[name] = c
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.waiting, name)
		n.mu.Unlock()
		close(c.done)
	}()

	ctx, cancel := context.WithTimeout(r.Context(), takeTimeout)
	defer cancel()
	if err := wsjson.Write(ctx, reservation, incoming{Connection: name}); err != nil {
		http.Error(w, "the node's reservation is gone", http.StatusServiceUnavailable)
		return
	}
	var taken net.Conn
	select {
	case taken = <-c.taken:
	case <-ctx.Done():
		http.Error(w, "the node didn't take the connection", http.StatusGatewayTimeout)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		taken.Close()
		return
	}
	bridge(websocket.NetConn(r.Context(), ws, websocket.MessageBinary), taken)
}

// serveAccept hands the node at the other end the caller's connection it was
// told about.
func (n *Network) serveAccept(w http.ResponseWriter, r *http.Request) {
	c := find(n, n.waiting, r.URL.Query().Get("connection"))
	if c == nil {
		http.Error(w, "no such connection", http.StatusNotFound)
		return
	}
	if peerNode(*r.TLS) != c.node {
		http.Error(w, "the connection is for another node", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
	select {
	case c.taken <- conn:
		<-c.done
	case <-c.done:
		conn.Close()
	}
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
