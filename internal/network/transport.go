package network

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"go-decentralized/internal/api"
)

// Messages and streams are HTTP/1.1 requests over mutual TLS, each over a
// connection of its own, so they work over any connection: TCP, or a stream
// through a relay. Streams upgrade the connection and hand it over raw.
const (
	messagesPath    = "/v1/messages/"
	streamsPath     = "/v1/streams/"
	dialTimeout     = 3 * time.Second
	exchangeTimeout = 10 * time.Second
	upgradeProtocol = "go-decentralized"
)

type (
	remoteAddrKey struct{}
	remoteIDKey   struct{}
)

// RemoteAddr returns the address a message being handled came from.
func RemoteAddr(ctx context.Context) string {
	addr, _ := ctx.Value(remoteAddrKey{}).(string)
	return addr
}

// RemoteID returns the ID of the node a message being handled came from, as
// it proved over TLS.
func RemoteID(ctx context.Context) string {
	id, _ := ctx.Value(remoteIDKey{}).(string)
	return id
}

// Send delivers req, as the message called name, to the node at any of to's
// addresses, tried in order, and decodes its reply into resp.
func (n *Network) Send(ctx context.Context, to api.Peer, name string, req, resp any) error {
	return tryEach(to, req, func(addr, expect string, body []byte) error {
		start := time.Now()
		answered, err := n.send(ctx, addr, expect, name, body, resp)
		n.trace(ctx, "message", name, addr, answered, start, err)
		return err
	})
}

// send delivers one message to addr, returning the node that answered.
func (n *Network) send(ctx context.Context, addr, expect, name string, body []byte, resp any) (string, error) {
	conn, res, answered, err := n.post(ctx, addr, expect, messagesPath+name, body, false)
	if err != nil {
		return answered, err
	}
	defer conn.Close()
	if err := wantStatus(res, http.StatusOK); err != nil {
		return answered, err
	}
	return answered, json.NewDecoder(res.Body).Decode(resp)
}

// openStream sends req to the handler of streams called name on the node at
// any of to's addresses. If the handler accepts, the returned connection is
// a raw stream to it.
func (n *Network) openStream(ctx context.Context, to api.Peer, name string, req any) (net.Conn, error) {
	var stream net.Conn
	err := tryEach(to, req, func(addr, expect string, body []byte) error {
		start := time.Now()
		conn, res, answered, err := n.post(ctx, addr, expect, streamsPath+name, body, true)
		if err == nil {
			if err = wantStatus(res, http.StatusSwitchingProtocols); err != nil {
				conn.Close()
			}
		}
		n.trace(ctx, "stream", name, addr, answered, start, err)
		if err != nil {
			return err
		}
		_ = conn.SetDeadline(time.Time{})
		stream = conn
		return nil
	})
	return stream, err
}

// tryEach marshals req and calls try with each of to's addresses in turn,
// and the node it must reach there, until one succeeds.
func tryEach(to api.Peer, req any, try func(addr, expect string, body []byte) error) error {
	if len(to.Addrs) == 0 {
		return errors.New("no address")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	var errs []error
	for _, addr := range to.Addrs {
		expect := to.ID
		if _, target, ok := relayAddr(addr); ok {
			if expect != "" && target != expect {
				errs = append(errs, fmt.Errorf("%s: address of another node", addr))
				continue
			}
			expect = target
		}
		if err := try(addr, expect, body); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", addr, err))
			continue
		}
		return nil
	}
	return errors.Join(errs...)
}

// post connects to addr over TLS, requiring the node there to be expect if
// set, sends body as a POST to path and reads the response head, giving up
// if ctx ends first. It returns the node that answered. The caller owns the
// connection, on which reading the body is bounded by exchangeTimeout.
func (n *Network) post(ctx context.Context, addr, expect, path string, body []byte, upgrade bool) (_ net.Conn, _ *http.Response, answered string, err error) {
	raw, err := n.dial(ctx, addr)
	if err != nil {
		return nil, nil, "", err
	}
	conn := tls.Client(raw, n.tlsConfig(expect))
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(exchangeTimeout))
	if err := conn.HandshakeContext(ctx); err != nil {
		return nil, nil, "", err
	}
	answered = peerNode(conn.ConnectionState())

	req, err := http.NewRequest(http.MethodPost, "http://node"+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, answered, err
	}
	req.Header.Set("Content-Type", "application/json")
	if upgrade {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", upgradeProtocol)
	} else {
		req.Close = true
	}
	if err := req.Write(conn); err != nil {
		return nil, nil, answered, err
	}
	r := bufio.NewReader(conn)
	res, err := http.ReadResponse(r, req)
	if err != nil {
		return nil, nil, answered, err
	}
	return &bufferedConn{Conn: conn, r: r}, res, answered, nil
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

// wantStatus returns an error, with the response body as message, unless res
// has the wanted status.
func wantStatus(res *http.Response, status int) error {
	if res.StatusCode == status {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	return fmt.Errorf("%s: %s", res.Status, bytes.TrimSpace(msg))
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

// serve serves the messages and streams other nodes send over connections
// from l, e.g. a listener or a relay reservation, until l closes.
func (n *Network) serve(l net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+messagesPath+"{name}", n.serveMessage)
	mux.HandleFunc("POST "+streamsPath+"{name}", n.serveStream)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return srv.Serve(tls.NewListener(l, n.tlsConfig("")))
}

func (n *Network) serveMessage(w http.ResponseWriter, r *http.Request) {
	h := find(n, n.handlers, r.PathValue("name"))
	if h == nil {
		http.Error(w, "unknown message", http.StatusNotFound)
		return
	}
	resp, err := h(handlerContext(r), decoder(w, r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (n *Network) serveStream(w http.ResponseWriter, r *http.Request) {
	h := find(n, n.streams, r.PathValue("name"))
	if h == nil {
		http.Error(w, "unknown stream", http.StatusNotFound)
		return
	}
	serve, err := h(handlerContext(r), decoder(w, r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, _ = io.Copy(io.Discard, r.Body) // leave nothing of the request in the stream
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if _, err := io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: "+upgradeProtocol+"\r\n\r\n"); err != nil {
		conn.Close()
		return
	}
	serve(&bufferedConn{Conn: conn, r: rw.Reader})
}

func handlerContext(r *http.Request) context.Context {
	ctx := context.WithValue(r.Context(), remoteAddrKey{}, r.RemoteAddr)
	if r.TLS != nil {
		ctx = context.WithValue(ctx, remoteIDKey{}, peerNode(*r.TLS))
	}
	return ctx
}

func decoder(w http.ResponseWriter, r *http.Request) func(any) error {
	return func(v any) error { return json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v) }
}

// bufferedConn is a connection whose reads first drain r, which may hold
// bytes read ahead while parsing HTTP.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
