package node

import (
	"context"
	"net"
	"testing"
	"time"

	"go-decentralized/internal/api"
	"go-decentralized/internal/identity"
	"go-decentralized/internal/module"
	"go-decentralized/internal/network"
)

// echo is a module that answers echo.hello with who sent the message.
type echo struct{}

func (echo) Name() string                      { return "echo" }
func (echo) Capabilities() []module.Capability { return nil }
func (echo) Messages() map[string]module.Handler {
	return map[string]module.Handler{
		"hello": module.HandlerFor(func(ctx context.Context, _ struct{}) (string, error) {
			return module.Sender(ctx), nil
		}),
	}
}

func TestModulesReceiveTheirMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, _ := start(ctx, t)
	b, bAddr := start(ctx, t)

	var sender string
	var err error
	for range 50 { // until b listens
		err = a.Env.Send(ctx, api.Peer{ID: b.Env.NodeID, Addrs: []string{bAddr}}, "echo.hello", struct{}{}, &sender)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if sender != a.Env.NodeID {
		t.Fatalf("echo saw the message come from %.8s, want %.8s", sender, a.Env.NodeID)
	}
}

// start runs a node with the echo module, listening on localhost.
func start(ctx context.Context, t *testing.T) (*Node, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	key, _ := identity.Load("")
	nw, err := network.New(network.Config{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	newEcho := func(func(any) error, module.Env) (module.Module, error) { return echo{}, nil }
	n, err := New(Config{Name: "test", Modules: []ModuleConfig{{Name: "echo"}}}, key, nw,
		map[string]module.Factory{"echo": newEcho})
	if err != nil {
		t.Fatal(err)
	}
	go nw.ListenAndServe(ctx, addr)
	return n, addr
}
