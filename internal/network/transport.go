package network

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// An address is the WebSocket URL a caller dials: ws:// or wss://, whose
// outer TLS is dressing that carriers and firewalls see; nodes prove
// themselves and encrypt inside (noise.go). Sessions carry calls
// (session.go); relays forward messages (relay.go).
const (
	sessionPath      = "/v1/session"
	reservePath      = "/v1/relay/reserve"
	relayPrefix      = "/v1/relay/" // + <node ID>: the node, through the relay
	acceptPath       = "/v1/relay/accept"
	dialTimeout      = 3 * time.Second
	handshakeTimeout = 10 * time.Second
	// subprotocol is the WebSocket subprotocol of sessions, and its
	// version: Noise-encrypted JSON-RPC 2.0.
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
// it proved in the session's handshake.
func RemoteID(ctx context.Context) string {
	id, _ := ctx.Value(remoteIDKey{}).(string)
	return id
}

// Scheme returns the scheme of this node's own addresses.
func (n *Network) Scheme() string {
	if n.cfg.Plaintext {
		return "ws"
	}
	return "wss"
}

// dialWS opens a WebSocket at the URL u. The outer TLS of wss URLs isn't
// verified: it's dressing, and the other end proves itself inside.
func (n *Network) dialWS(ctx context.Context, u string) (*websocket.Conn, error) {
	if !strings.HasPrefix(u, "ws://") && !strings.HasPrefix(u, "wss://") {
		return nil, errors.New("not a WebSocket URL")
	}
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{
		DialContext:     (&net.Dialer{Timeout: dialTimeout}).DialContext,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: client, Subprotocols: []string{subprotocol}})
	return ws, err
}

// sessionURL returns the URL that opens a session at addr: relayed addresses
// are dialed as they are, the relay connects them on.
func sessionURL(addr string) string {
	if isRelay(addr) {
		return addr
	}
	return addr + sessionPath
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
// connections, other nodes open over connections from l, until l closes.
// Unless the node is configured for plaintext, e.g. behind a platform's own
// TLS, it wraps them in TLS with a self-signed certificate, so its traffic
// looks like HTTPS to firewalls; the certificate proves nothing, sessions
// do.
func (n *Network) serve(l net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+sessionPath, n.serveSession)
	if n.cfg.Relay.Serve {
		mux.HandleFunc("GET "+reservePath, n.serveReserve)
		mux.HandleFunc("GET "+acceptPath, n.serveAccept)
		mux.HandleFunc("GET "+relayPrefix+"{id}", n.serveConnect)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if n.cfg.Plaintext {
		return srv.Serve(l)
	}
	return srv.Serve(tls.NewListener(l, &tls.Config{
		Certificates: []tls.Certificate{n.cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
	}))
}

// accept takes the WebSocket of an incoming session and runs the answering
// side of its handshake.
func (n *Network) accept(w http.ResponseWriter, r *http.Request) (string, *secured, error) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{subprotocol}})
	if err != nil {
		return "", nil, err
	}
	if ws.Subprotocol() != subprotocol {
		ws.Close(websocket.StatusPolicyViolation, "sessions speak "+subprotocol)
		return "", nil, errors.New("no common subprotocol")
	}
	peer, sc, err := n.secure(context.Background(), ws, false, "")
	if err != nil {
		ws.CloseNow()
		return "", nil, err
	}
	return peer, sc, nil
}

func (n *Network) serveSession(w http.ResponseWriter, r *http.Request) {
	peer, sc, err := n.accept(w, r)
	if err != nil {
		return
	}
	n.startSession(sc, peer, r.RemoteAddr, false, true)
}
