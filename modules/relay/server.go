package relay

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"slices"

	"github.com/hashicorp/yamux"
)

// handleReserve accepts a node's reservation and holds it until it closes.
func (m *Module) handleReserve(_ context.Context, req request) (func(net.Conn), error) {
	return func(conn net.Conn) {
		session, err := yamux.Client(conn, nil)
		if err != nil {
			conn.Close()
			return
		}
		m.mu.Lock()
		if old := m.sessions[req.ID]; old != nil {
			old.Close()
		}
		m.sessions[req.ID] = session
		m.mu.Unlock()

		slog.Info("relay: reservation opened", "node", req.ID)
		<-session.CloseChan()
		slog.Info("relay: reservation closed", "node", req.ID)

		m.mu.Lock()
		if m.sessions[req.ID] == session {
			delete(m.sessions, req.ID)
		}
		m.mu.Unlock()
	}, nil
}

// handleConnect bridges a caller onto a new stream over the reservation of
// the node it asks for.
func (m *Module) handleConnect(_ context.Context, req request) (func(net.Conn), error) {
	m.mu.Lock()
	session := m.sessions[req.ID]
	m.mu.Unlock()
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

// reservations returns the IDs of the nodes holding a reservation here.
func (m *Module) reservations() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Sorted(maps.Keys(m.sessions))
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
