package network

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"go-decentralized/module"
)

// testNetwork makes a node's network that serves its own capabilities, and
// test.whoami, which answers with the caller as TLS proved it. Private:
// tests run on loopback.
func testNetwork(t *testing.T, port int, relay RelayConfig) *Network {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(Config{Key: key, ListenPort: port, Private: true, Relay: relay})
	if err != nil {
		t.Fatal(err)
	}
	own := n.Handlers()
	n.SetHandler(func(ctx context.Context, ref string, body json.RawMessage) (json.RawMessage, error) {
		if ref == "test.whoami" {
			return json.Marshal(map[string]string{"id": RemoteID(ctx)})
		}
		name, _ := strings.CutPrefix(ref, "network.")
		h := own[name]
		if h == nil {
			return nil, module.Errorf(module.CodeNotFound, "no capability %s", ref)
		}
		out, err := h(ctx, func(v any) error { return module.Decode(body, v) })
		if err != nil {
			return nil, err
		}
		return module.Encode(out)
	})
	return n
}

// listening makes a network that accepts connections on localhost.
func listening(t *testing.T, relay RelayConfig) (*Network, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	n := testNetwork(t, l.Addr().(*net.TCPAddr).Port, relay)
	go n.serve(l)
	return n, l.Addr().String()
}

// whoami calls test.whoami on to and returns who it saw calling.
func whoami(ctx context.Context, from *Network, to module.Peer) (string, error) {
	result, err := from.Call(ctx, to, "test.whoami", nil)
	var out struct{ ID string }
	if err == nil {
		err = json.Unmarshal(result, &out)
	}
	return out.ID, err
}

func TestCallProvesBothEnds(t *testing.T) {
	ctx := context.Background()
	a, _ := listening(t, RelayConfig{})
	b, bAddr := listening(t, RelayConfig{})
	c, _ := listening(t, RelayConfig{})

	caller, err := whoami(ctx, a, module.Peer{ID: b.id, Addrs: []string{bAddr}})
	if err != nil {
		t.Fatal(err)
	}
	if caller != a.id {
		t.Fatalf("b saw the call come from %.8s, want %.8s", caller, a.id)
	}
	if _, err := whoami(ctx, a, module.Peer{ID: c.id, Addrs: []string{bAddr}}); module.Code(err) != module.CodeUnavailable {
		t.Fatalf("reaching b while expecting c: err = %v", err)
	}

	traces := a.Traces(time.Time{})
	if len(traces) != 2 || traces[0].To != b.id || traces[0].Error != "" || traces[1].Error == "" {
		t.Fatalf("traces = %+v", traces)
	}
}

func TestCallsShareASession(t *testing.T) {
	ctx := context.Background()
	a, _ := listening(t, RelayConfig{})
	b, bAddr := listening(t, RelayConfig{})
	for range 3 {
		if _, err := whoami(ctx, a, module.Peer{ID: b.id, Addrs: []string{bAddr}}); err != nil {
			t.Fatal(err)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sessions) != 1 || len(a.sessions[b.id]) != 1 {
		t.Fatalf("a has sessions %v, want one with b", a.sessions)
	}
}

// A node that accepts no connections, e.g. behind NAT, can be called back
// over the session it opened: no address needed.
func TestCalledBackOverItsSession(t *testing.T) {
	ctx := context.Background()
	client := testNetwork(t, 0, RelayConfig{})
	server, serverAddr := listening(t, RelayConfig{})
	if _, err := whoami(ctx, client, module.Peer{ID: server.id, Addrs: []string{serverAddr}}); err != nil {
		t.Fatal(err)
	}
	seen, err := whoami(ctx, server, module.Peer{ID: client.id})
	if err != nil {
		t.Fatal(err)
	}
	if seen != server.id {
		t.Fatalf("client saw the call come from %.8s, want the server %.8s", seen, server.id)
	}
}

func TestErrorsComeBackAsSent(t *testing.T) {
	a, _ := listening(t, RelayConfig{})
	b, bAddr := listening(t, RelayConfig{})
	_, err := a.Call(context.Background(), module.Peer{ID: b.id, Addrs: []string{bAddr}}, "test.missing", nil)
	if module.Code(err) != module.CodeNotFound {
		t.Fatalf("err = %v, want code %s", err, module.CodeNotFound)
	}
}

func TestRelayIsEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay, relayAddr := listening(t, RelayConfig{Serve: true})
	// target accepts no connections: it's only reachable through the relay.
	target := testNetwork(t, 0, RelayConfig{Via: []string{relayAddr}})
	go target.Run(ctx)
	caller, _ := listening(t, RelayConfig{})

	addr := waitFor(t, func() string {
		for _, a := range target.Addrs() {
			if isRelay(a) {
				return a
			}
		}
		return ""
	})
	if find(relay, relay.reservations, target.id) == nil {
		t.Fatal("relay holds no reservation under target's proven ID")
	}
	seen, err := whoami(ctx, caller, module.Peer{ID: target.id, Addrs: []string{addr}})
	if err != nil {
		t.Fatal(err)
	}
	// Had the relay terminated TLS, target would see the relay as caller.
	if seen != caller.id {
		t.Fatalf("target saw the call come from %.8s, want the caller %.8s", seen, caller.id)
	}
	// Target knows the session goes through the relay, so it would dial the
	// caller directly rather than call back through the relay.
	target.mu.Lock()
	defer target.mu.Unlock()
	if ss := target.sessions[caller.id]; len(ss) != 1 || !isRelay(ss[0].addr) {
		t.Fatalf("target's sessions with the caller: %v", ss)
	}
}

func TestDialBack(t *testing.T) {
	ctx := context.Background()
	peer, peerAddr := listening(t, RelayConfig{})
	peers := []module.Peer{{ID: peer.id, Addrs: []string{peerAddr}}}

	reachable, _ := listening(t, RelayConfig{})
	if !reachable.checkReachability(ctx, peers) || len(reachable.DirectAddrs()) != 1 {
		t.Fatalf("listening node isn't reachable: %v", reachable.DirectAddrs())
	}
	unreachable := testNetwork(t, 1, RelayConfig{}) // nothing listens on port 1
	if !unreachable.checkReachability(ctx, peers) || len(unreachable.DirectAddrs()) != 0 {
		t.Fatalf("node that doesn't listen is reachable at %v", unreachable.DirectAddrs())
	}
	if known := unreachable.peers(); len(known) != 1 || known[0].ID != peer.id {
		t.Fatalf("peers to ask for dial-backs = %v, want the one that answered", known)
	}
}

// waitFor polls f until it returns something.
func waitFor(t *testing.T, f func() string) string {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if s := f(); s != "" {
			return s
		}
	}
	t.Fatal("timed out")
	return ""
}
