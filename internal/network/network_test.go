package network

import (
	"context"
	"crypto/ed25519"
	"net"
	"strings"
	"testing"
	"time"

	"go-decentralized/internal/api"
)

// testNetwork makes a node's network, with a test.whoami message that
// answers with the sender as TLS proved it. Private: tests run on loopback.
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
	handle(n, "test.whoami", func(ctx context.Context, _ struct{}) (string, error) {
		return RemoteID(ctx), nil
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

func TestSendProvesBothEnds(t *testing.T) {
	ctx := context.Background()
	a, _ := listening(t, RelayConfig{})
	b, bAddr := listening(t, RelayConfig{})
	c, _ := listening(t, RelayConfig{})

	var sender string
	if err := a.Send(ctx, api.Peer{ID: b.id, Addrs: []string{bAddr}}, "test.whoami", struct{}{}, &sender); err != nil {
		t.Fatal(err)
	}
	if sender != a.id {
		t.Fatalf("b saw the message come from %.8s, want %.8s", sender, a.id)
	}
	if err := a.Send(ctx, api.Peer{ID: c.id, Addrs: []string{bAddr}}, "test.whoami", struct{}{}, &sender); err == nil {
		t.Fatal("reached b while expecting c")
	}

	traces := a.Traces(time.Time{})
	if len(traces) != 2 || traces[0].To != b.id || traces[0].Error != "" || traces[1].Error == "" {
		t.Fatalf("traces = %+v", traces)
	}
}

func TestRelayIsEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay, relayAddr := listening(t, RelayConfig{Serve: true})
	// target accepts no connections: it's only reachable through the relay.
	target := testNetwork(t, 0, RelayConfig{Via: []string{relayAddr}})
	go target.Run(ctx, func(context.Context) []api.Peer { return nil })
	caller, _ := listening(t, RelayConfig{})

	addr := waitFor(t, func() string {
		for _, a := range target.Addrs() {
			if strings.HasPrefix(a, relayScheme+"/") {
				return a
			}
		}
		return ""
	})
	if find(relay, relay.sessions, target.id) == nil {
		t.Fatal("relay holds no reservation under target's proven ID")
	}
	var sender string
	if err := caller.Send(ctx, api.Peer{ID: target.id, Addrs: []string{addr}}, "test.whoami", struct{}{}, &sender); err != nil {
		t.Fatal(err)
	}
	// Had the relay terminated TLS, target would see the relay as sender.
	if sender != caller.id {
		t.Fatalf("target saw the message come from %.8s, want the caller %.8s", sender, caller.id)
	}
}

func TestDialBack(t *testing.T) {
	ctx := context.Background()
	peer, peerAddr := listening(t, RelayConfig{})
	peers := []api.Peer{{ID: peer.id, Addrs: []string{peerAddr}}}

	reachable, _ := listening(t, RelayConfig{})
	if !reachable.checkReachability(ctx, peers) || len(reachable.DirectAddrs()) != 1 {
		t.Fatalf("listening node isn't reachable: %v", reachable.DirectAddrs())
	}
	unreachable := testNetwork(t, 1, RelayConfig{}) // nothing listens on port 1
	if !unreachable.checkReachability(ctx, peers) || len(unreachable.DirectAddrs()) != 0 {
		t.Fatalf("node that doesn't listen is reachable at %v", unreachable.DirectAddrs())
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
