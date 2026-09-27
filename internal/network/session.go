package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"go-decentralized/module"
)

// Calls travel over sessions: one lasting WebSocket per peer, which carries
// calls both ways as JSON-RPC (see module.Link). Either node calls the other
// over a session, whichever dialed it, so a node behind NAT that connected
// to a peer can be called back without a relay.
const (
	callTimeout = 10 * time.Second // for calls without a deadline of their own
	keepEvery   = 30 * time.Second // how often sessions ping their peer
	idleTimeout = 5 * time.Minute  // sessions without calls for this long close
)

type session struct {
	peer     string // the peer's ID, as it proved over TLS
	addr     string // the address dialed, or the peer's, for sessions it dialed
	outbound bool
	ws       *websocket.Conn
	link     *module.Link
	done     chan struct{}
	used     atomic.Int64 // when the last call was made, either way, in Unix nanoseconds
}

func (s *session) close() { s.link.Close() }

// dialing is a session being dialed, for callers of the same peer to share.
type dialing struct {
	done chan struct{}
	s    *session
	err  error
}

// Call calls the capability ref on the node to, with body as its input, and
// returns its result. It uses the session it has with the node, if any, else
// dials one at the first of to's addresses that answers. Errors the node
// answers with are *module.Error; if none answers, the error's code is
// module.CodeUnavailable.
func (n *Network) Call(ctx context.Context, to module.Peer, ref string, body json.RawMessage) (json.RawMessage, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, callTimeout)
		defer cancel()
	}
	s, err := n.session(ctx, to, ref)
	if err != nil {
		return nil, module.Errorf(module.CodeUnavailable, "%v", err)
	}
	return n.call(ctx, s, ref, body)
}

// call makes a call over s.
func (n *Network) call(ctx context.Context, s *session, ref string, body json.RawMessage) (json.RawMessage, error) {
	start := time.Now()
	s.used.Store(start.UnixNano())
	result, err := s.link.Call(ctx, module.Call{Ref: ref, Input: body})
	n.trace(ctx, "call", ref, s.addr, s.peer, start, err)
	return result, err
}

// session returns a session with the node to: one it has, or a new one.
// Sessions through a relay are only used if to has no direct address, or
// dialing it failed. Callers of the same node share its dial.
func (n *Network) session(ctx context.Context, to module.Peer, ref string) (*session, error) {
	key := to.ID
	if key == "" {
		key = strings.Join(to.Addrs, " ")
	}
	n.mu.Lock()
	if s := n.sessionTo(to); s != nil && (!isRelay(s.addr) || !direct(to)) {
		n.mu.Unlock()
		return s, nil
	}
	if d := n.dialing[key]; d != nil {
		n.mu.Unlock()
		select {
		case <-d.done:
			return d.s, d.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	d := &dialing{done: make(chan struct{})}
	n.dialing[key] = d
	n.mu.Unlock()

	d.s, d.err = n.dialSession(ctx, to, ref)
	n.mu.Lock()
	if d.err != nil {
		// A direct dial failed, but there may be a way through a relay.
		if s := n.sessionTo(to); s != nil {
			d.s, d.err = s, nil
		}
	}
	delete(n.dialing, key)
	n.mu.Unlock()
	close(d.done)
	return d.s, d.err
}

// direct reports whether to has a direct address.
func direct(to module.Peer) bool {
	return slices.ContainsFunc(to.Addrs, func(a string) bool { return !isRelay(a) })
}

// sessionTo returns a session with the node to, if any, preferring direct
// ones to ones through a relay. n.mu must be held.
func (n *Network) sessionTo(to module.Peer) *session {
	if to.ID != "" {
		ss := n.sessions[to.ID]
		if i := slices.IndexFunc(ss, func(s *session) bool { return !isRelay(s.addr) }); i >= 0 {
			return ss[i]
		}
		if len(ss) > 0 {
			return ss[0]
		}
		return nil
	}
	for _, ss := range n.sessions {
		for _, s := range ss {
			if s.outbound && slices.Contains(to.Addrs, s.addr) {
				return s
			}
		}
	}
	return nil
}

// dialSession dials a session at the first of to's addresses that answers.
func (n *Network) dialSession(ctx context.Context, to module.Peer, ref string) (*session, error) {
	var errs []error
	for _, addr := range to.Addrs {
		expect, err := expected(to, addr)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		start := time.Now()
		s, err := n.openSession(ctx, addr, expect, true)
		if err != nil {
			n.trace(ctx, "call", ref, addr, "", start, err)
			errs = append(errs, fmt.Errorf("%s: %w", addr, err))
			continue
		}
		return s, nil
	}
	if len(errs) == 0 {
		return nil, errors.New("no address")
	}
	return nil, errors.Join(errs...)
}

// expected returns the node a caller must reach at addr: to's, or the one a
// relay address names.
func expected(to module.Peer, addr string) (string, error) {
	if _, target, ok := relayAddr(addr); ok {
		if to.ID != "" && target != to.ID {
			return "", fmt.Errorf("%s: address of another node", addr)
		}
		return target, nil
	}
	return to.ID, nil
}

// openSession dials a session at addr with the node expect, if set. Pooled
// sessions serve later calls too; others are the caller's to close.
func (n *Network) openSession(ctx context.Context, addr, expect string, pooled bool) (*session, error) {
	ws, answered, err := n.connect(ctx, addr, expect, sessionPath, subprotocol)
	if err != nil {
		return nil, err
	}
	if ws.Subprotocol() != subprotocol {
		ws.CloseNow()
		return nil, fmt.Errorf("%.8s doesn't speak %s", answered, subprotocol)
	}
	if !isRelay(addr) {
		n.remember(module.Peer{ID: answered, Addrs: []string{addr}})
	}
	return n.startSession(ws, answered, addr, true, pooled), nil
}

// startSession carries calls over ws, a WebSocket with peer, until it
// closes.
func (n *Network) startSession(ws *websocket.Conn, peer, addr string, outbound, pooled bool) *session {
	ws.SetReadLimit(module.MaxMessage)
	s := &session{peer: peer, addr: addr, outbound: outbound, ws: ws, done: make(chan struct{})}
	s.used.Store(time.Now().UnixNano())
	ctx := context.WithValue(context.Background(), remoteIDKey{}, peer)
	ctx = context.WithValue(ctx, remoteAddrKey{}, addr)
	s.link = module.NewLink(ctx, jsonStream{ws}, func(ctx context.Context, call module.Call) (json.RawMessage, error) {
		s.used.Store(time.Now().UnixNano())
		n.mu.Lock()
		h := n.handler
		n.mu.Unlock()
		if h == nil {
			return nil, module.Errorf(module.CodeUnavailable, "not ready")
		}
		return h(ctx, call.Ref, call.Input)
	})
	if pooled {
		n.mu.Lock()
		n.sessions[peer] = append(n.sessions[peer], s)
		n.mu.Unlock()
		go n.keep(s)
	}
	go func() {
		<-s.link.Done()
		n.mu.Lock()
		n.sessions[peer] = slices.DeleteFunc(n.sessions[peer], func(o *session) bool { return o == s })
		if len(n.sessions[peer]) == 0 {
			delete(n.sessions, peer)
		}
		n.mu.Unlock()
		close(s.done)
	}()
	return s
}

// keep closes s once it's unused for idleTimeout, or once its peer stops
// answering WebSocket pings. The pings also keep NAT mappings open.
func (n *Network) keep(s *session) {
	t := time.NewTicker(keepEvery)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, s.used.Load())) > idleTimeout {
			s.close()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), keepEvery/3)
		err := s.ws.Ping(ctx)
		cancel()
		if err != nil {
			s.close()
			return
		}
	}
}

// closeSessions closes every session.
func (n *Network) closeSessions() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, ss := range n.sessions {
		for _, s := range ss {
			s.close()
		}
	}
}

// jsonStream carries JSON-RPC messages over a WebSocket, one per text
// message.
type jsonStream struct{ ws *websocket.Conn }

func (s jsonStream) WriteObject(v any) error { return wsjson.Write(context.Background(), s.ws, v) }
func (s jsonStream) ReadObject(v any) error  { return wsjson.Read(context.Background(), s.ws, v) }
func (s jsonStream) Close() error            { return s.ws.CloseNow() }
