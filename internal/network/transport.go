package network

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Nodes connect over TLS, both proving their ID (tls.go), and speak WebSocket
// over it, so connections look like HTTPS to firewalls and proxies, and work
// over anything: TCP, or a stream through a relay. Sessions carry calls
// (session.go); relays carry raw bytes (relay.go).
const (
	sessionPath      = "/v1/session"
	reservePath      = "/v1/relay/reserve"
	connectPath      = "/v1/relay/connect"
	acceptPath       = "/v1/relay/accept"
	dialTimeout      = 3 * time.Second
	handshakeTimeout = 10 * time.Second
	// subprotocol is the WebSocket subprotocol of sessions, and its
	// version: JSON-RPC 2.0, a message per text message.
	subprotocol = "decentralized.v1"
)

type (
	remoteAddrKey struct{}
	remoteIDKey   struct{}
)

// RemoteAddr returns the address a call being handled came from.
func RemoteAddr(ctx context.Context) string {
	addr, _ := ctx.Value(remoteAddrKey{}).(string)
	return addr
}

// RemoteID returns the ID of the node that made the call being handled, as
// it proved over TLS.
func RemoteID(ctx context.Context) string {
	id, _ := ctx.Value(remoteIDKey{}).(string)
	return id
}

// connect opens a WebSocket to the node at addr, at path, requiring the node
// to be expect if set. It returns the WebSocket and the node that answered.
func (n *Network) connect(ctx context.Context, addr, expect, path string, subprotocols ...string) (*websocket.Conn, string, error) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	var answered string
	client := &http.Client{Transport: &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := n.dialTLS(ctx, addr, expect)
			if err == nil {
				answered = peerNode(conn.ConnectionState())
			}
			return conn, err
		},
	}}
	c, _, err := websocket.Dial(ctx, "wss://node"+path, &websocket.DialOptions{HTTPClient: client, Subprotocols: subprotocols})
	return c, answered, err
}

// dialTLS connects to addr over TLS, requiring the node there to be expect
// if set.
func (n *Network) dialTLS(ctx context.Context, addr, expect string) (*tls.Conn, error) {
	raw, err := n.dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, n.tlsConfig(expect))
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

// dial connects to addr: over TCP, or through a relay for relay addresses.
func (n *Network) dial(ctx context.Context, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	if relay, target, ok := relayAddr(addr); ok {
		return n.dialRelay(ctx, relay, target)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

// ListenAndServe accepts connections from other nodes on addr until ctx is
// done.
func (n *Network) ListenAndServe(ctx context.Context, addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	if err := n.serve(l); ctx.Err() == nil {
		return err
	}
	return nil
}

// serve serves the sessions, and as a relay the reservations and relayed
// connections, other nodes open over connections from l, e.g. a listener or
// a relay reservation, until l closes.
func (n *Network) serve(l net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+sessionPath, n.serveSession)
	if n.cfg.Relay.Serve {
		mux.HandleFunc("GET "+reservePath, n.serveReserve)
		mux.HandleFunc("GET "+connectPath, n.serveConnect)
		mux.HandleFunc("GET "+acceptPath, n.serveAccept)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return srv.Serve(tls.NewListener(l, n.tlsConfig("")))
}

func (n *Network) serveSession(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{subprotocol}})
	if err != nil {
		return
	}
	if c.Subprotocol() != subprotocol {
		c.Close(websocket.StatusPolicyViolation, "sessions speak "+subprotocol)
		return
	}
	peer, addr := peerNode(*r.TLS), r.RemoteAddr
	if strings.HasPrefix(addr, relayScheme+"/") {
		addr += "/" + peer // came through a relay, see streams
	}
	n.startSession(c, peer, addr, false, true)
}
