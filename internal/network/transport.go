package network

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Messages and streams are HTTP requests, each over a connection of its own,
// so they work over any connection a transport provides. Streams upgrade the
// connection and hand it over raw.
const (
	// MessagesPath and StreamsPath are where Handler serves messages
	// (POST MessagesPath<name>) and streams (POST StreamsPath<name>).
	MessagesPath = "/v1/messages/"
	StreamsPath  = "/v1/streams/"

	dialTimeout     = 3 * time.Second
	exchangeTimeout = 10 * time.Second
	upgradeProtocol = "go-decentralized"
	// nodeIDHeader carries the sender's node ID on requests and the answering
	// node's on responses. It isn't authenticated yet, see TODO.md.
	nodeIDHeader = "Node-Id"
)

type remoteAddrKey struct{}

// RemoteAddr returns the address a message or stream being handled came
// from.
func RemoteAddr(ctx context.Context) string {
	addr, _ := ctx.Value(remoteAddrKey{}).(string)
	return addr
}

func (n *Network) Send(ctx context.Context, addrs []string, name string, req, resp any) error {
	return tryEach(addrs, req, func(addr string, body []byte) error {
		start := time.Now()
		to, err := n.send(ctx, addr, name, body, resp)
		n.trace(ctx, "message", name, addr, to, start, err)
		return err
	})
}

// send delivers one message to addr, returning the ID of the node that
// answered.
func (n *Network) send(ctx context.Context, addr, name string, body []byte, resp any) (string, error) {
	conn, res, err := n.post(ctx, addr, MessagesPath+name, body, false)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	to := res.Header.Get(nodeIDHeader)
	if err := expect(res, http.StatusOK); err != nil {
		return to, err
	}
	return to, json.NewDecoder(res.Body).Decode(resp)
}

// OpenStream sends req to the handler of streams called name on the node at
// any of addrs, tried in order. If the handler accepts, the returned
// connection is a raw stream to it.
func (n *Network) OpenStream(ctx context.Context, addrs []string, name string, req any) (net.Conn, error) {
	var stream net.Conn
	err := tryEach(addrs, req, func(addr string, body []byte) error {
		start := time.Now()
		conn, to, err := n.openStream(ctx, addr, name, body)
		n.trace(ctx, "stream", name, addr, to, start, err)
		stream = conn
		return err
	})
	return stream, err
}

// openStream opens one stream to addr, returning the ID of the node that
// answered.
func (n *Network) openStream(ctx context.Context, addr, name string, body []byte) (net.Conn, string, error) {
	conn, res, err := n.post(ctx, addr, StreamsPath+name, body, true)
	if err != nil {
		return nil, "", err
	}
	to := res.Header.Get(nodeIDHeader)
	if err := expect(res, http.StatusSwitchingProtocols); err != nil {
		conn.Close()
		return nil, to, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, to, nil
}

// tryEach marshals req and calls try with each address in turn until one
// succeeds.
func tryEach(addrs []string, req any, try func(addr string, body []byte) error) error {
	if len(addrs) == 0 {
		return errors.New("no address")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	var errs []error
	for _, addr := range addrs {
		err := try(addr, body)
		if err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", addr, err))
	}
	return errors.Join(errs...)
}

// post dials addr, sends body as a POST to path and reads the response head,
// giving up if ctx ends first. The caller owns the returned connection, on
// which reading the body is bounded by exchangeTimeout.
func (n *Network) post(ctx context.Context, addr, path string, body []byte, upgrade bool) (_ net.Conn, _ *http.Response, err error) {
	conn, err := n.dial(ctx, addr)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(exchangeTimeout))

	req, err := http.NewRequest(http.MethodPost, "http://node"+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(nodeIDHeader, n.cfg.ID)
	if upgrade {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", upgradeProtocol)
	} else {
		req.Close = true
	}
	if err := req.Write(conn); err != nil {
		return nil, nil, err
	}
	r := bufio.NewReader(conn)
	res, err := http.ReadResponse(r, req)
	if err != nil {
		return nil, nil, err
	}
	return &bufferedConn{Conn: conn, r: r}, res, nil
}

// dial connects to addr: "host:port" directly over TCP, "<scheme>/<rest>"
// through the dialer registered for scheme.
func (n *Network) dial(ctx context.Context, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	scheme, rest, ok := strings.Cut(addr, "/")
	if !ok {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
	dial := find(n, n.dialers, scheme)
	if dial == nil {
		return nil, fmt.Errorf("no transport for %q addresses", scheme)
	}
	return dial(ctx, rest)
}

// expect returns an error, with the response body as message, unless res
// has the wanted status.
func expect(res *http.Response, status int) error {
	if res.StatusCode == status {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	return fmt.Errorf("%s: %s", res.Status, bytes.TrimSpace(msg))
}

// Handler serves the messages and streams other nodes send. Mount it at
// MessagesPath and StreamsPath.
func (n *Network) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+MessagesPath+"{name}", n.serveMessage)
	mux.HandleFunc("POST "+StreamsPath+"{name}", n.serveStream)
	return mux
}

// Serve serves messages and streams on l until it closes. Transports use it
// for connections that don't arrive at the node's listener, e.g. through a
// relay reservation.
func (n *Network) Serve(l net.Listener) error {
	srv := &http.Server{Handler: n.Handler(), ReadHeaderTimeout: 5 * time.Second}
	return srv.Serve(l)
}

func (n *Network) serveMessage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(nodeIDHeader, n.cfg.ID)
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
	w.Header().Set(nodeIDHeader, n.cfg.ID)
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
	head := "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + upgradeProtocol +
		"\r\n" + nodeIDHeader + ": " + n.cfg.ID + "\r\n\r\n"
	if _, err := io.WriteString(conn, head); err != nil {
		conn.Close()
		return
	}
	serve(&bufferedConn{Conn: conn, r: rw.Reader})
}

func handlerContext(r *http.Request) context.Context {
	return context.WithValue(r.Context(), remoteAddrKey{}, r.RemoteAddr)
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
