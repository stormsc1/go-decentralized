package modules

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"go-decentralized/internal/identity"
	"go-decentralized/internal/network"
	"go-decentralized/internal/node"
)

// TestTypeScriptGreeter runs the TypeScript greeter (examples/greeter-ts) as
// a process module, in place of the Go one.
func TestTypeScriptGreeter(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("needs Node.js")
	}
	var cfg node.Config
	err := yaml.Unmarshal([]byte(`
name: test
modules:
  - name: greeter
    run: [node, ../examples/greeter-ts/greeter.ts]
    config: {greeting: Hi}
`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	n := newNode(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		n.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	greeted, err := n.Subscribe("greeter.greeted")
	if err != nil {
		t.Fatal(err)
	}
	defer greeted.Close()
	result, err := n.Call(ctx, "greeter.hello", json.RawMessage(`{"name": "you"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Greeting string }
	if err := json.Unmarshal(result, &out); err != nil || !strings.HasPrefix(out.Greeting, "Hi you, from test!") || !strings.HasSuffix(out.Greeting, "(TypeScript)") {
		t.Fatalf("greeting = %q, %v", out.Greeting, err)
	}
	e := <-greeted.Events()
	var body struct{ Name, Caller string }
	if err := json.Unmarshal(e.Body, &body); err != nil || body.Name != "you" || body.Caller != n.ID {
		t.Fatalf("event %s %s", e.Ref, e.Body)
	}
	if _, err := n.Call(ctx, "greeter.hello", json.RawMessage(`{"name": 1}`)); err == nil {
		t.Fatal("the node passed on an input the module's schema rejects")
	}
}

func newNode(t *testing.T, cfg node.Config) *node.Node {
	t.Helper()
	key, _ := identity.Load("")
	nw, err := network.New(network.Config{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.New(cfg, key, nw, Factories) // no data directory: stores in memory
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCatalogue loads every module into a node, which checks their
// manifests, schemas and handlers against each other.
func TestCatalogue(t *testing.T) {
	var cfg node.Config
	err := yaml.Unmarshal([]byte(`
name: test
network: {mdns: false}
modules:
  - name: debug
  - name: greeter
    config: {greeting: Hi}
`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	n := newNode(t, cfg)
	if got := len(n.Info().Modules); got != 4+len(Factories) {
		t.Fatalf("node runs %d modules, want the 4 built in plus %d", got, len(Factories))
	}
	result, err := n.Call(context.Background(), "greeter.hello", json.RawMessage(`{"name": "you"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Greeting string }
	if err := json.Unmarshal(result, &out); err != nil || !strings.HasPrefix(out.Greeting, "Hi you, from test!") {
		t.Fatalf("greeting = %q, %v", out.Greeting, err)
	}
}
