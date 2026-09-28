package vault

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"go-decentralized/did"
	"go-decentralized/internal/identity"
	"go-decentralized/internal/network"
	"go-decentralized/internal/node"
	"go-decentralized/module"
)

// caller calls the vault on a node, as its app would.
type caller struct {
	t *testing.T
	n *node.Node
}

func (c caller) call(name string, in any) (map[string]any, error) {
	c.t.Helper()
	input, _ := json.Marshal(in)
	result, err := c.n.Call(context.Background(), Name+"."+name, input)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(result, &out); err != nil {
		c.t.Fatal(err)
	}
	return out, nil
}

// save has root keep blob under proof, as the browser would, in the vault
// on node vault if set.
func (c caller) save(root ed25519.PrivateKey, proof []byte, blob any, vault string) (map[string]any, error) {
	c.t.Helper()
	data, _ := json.Marshal(map[string]any{"proof": proof, "blob": blob})
	signed, err := did.Sign(root, SavePurpose, data)
	if err != nil {
		c.t.Fatal(err)
	}
	in := map[string]any{"signed": signed}
	if vault != "" {
		in["vault"] = vault
	}
	return c.call("save", in)
}

func (c caller) open(proof []byte, vault string) (map[string]any, error) {
	c.t.Helper()
	in := map[string]any{"proof": proof}
	if vault != "" {
		in["vault"] = vault
	}
	return c.call("open", in)
}

func config(t *testing.T, name string) node.Config {
	t.Helper()
	cfg, err := node.ParseConfig(name, []byte(`
name: `+name+`
network: {mdns: false}
stores: {main: {path: ":memory:"}}
modules:
  - name: vault
    stores: {data: main}
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestVault(t *testing.T) {
	key, _ := identity.Load("")
	nw, err := network.New(network.Config{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.New(config(t, "test"), key, nw, map[string]module.Factory{Name: New})
	if err != nil {
		t.Fatal(err)
	}
	c := caller{t, n}

	_, root, _ := ed25519.GenerateKey(rand.Reader)
	proof := make([]byte, 32)
	rand.Read(proof)
	blob := map[string]any{"v": 1, "pub": "AAAA", "iv": "BBBB", "ct": "CCCC"}

	// Nothing until saved; then whoever has the proof opens it.
	if _, err := c.open(proof, ""); module.Code(err) != module.CodeNotFound {
		t.Fatalf("opened an account that doesn't exist: err = %v", err)
	}
	saved, err := c.save(root, proof, blob, "")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := c.open(proof, "")
	if err != nil {
		t.Fatal(err)
	}
	if opened["id"] != saved["id"] || opened["root"] != did.Key(root.Public().(ed25519.PublicKey)) || opened["node"] != n.ID || opened["blob"].(map[string]any)["ct"] != "CCCC" || opened["version"] != 1.0 {
		t.Fatalf("opened %+v", opened)
	}

	// Another proof is another account; a short one isn't a proof; a blob has
	// a size.
	other := make([]byte, 32)
	rand.Read(other)
	if _, err := c.open(other, ""); module.Code(err) != module.CodeNotFound {
		t.Fatalf("opened with another proof: err = %v", err)
	}
	if _, err := c.save(root, proof[:8], blob, ""); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("saved with a short proof: err = %v", err)
	}
	if _, err := c.save(root, proof, map[string]any{"ct": strings.Repeat("x", 9000)}, ""); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("saved a huge blob: err = %v", err)
	}

	// Only the root replaces its blob: not another root with the proof, and
	// not a device the root authorized.
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := c.save(stranger, proof, blob, ""); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("a stranger with the proof replaced the blob: err = %v", err)
	}
	devicePub, device, _ := ed25519.GenerateKey(rand.Reader)
	delegation, _ := did.Delegate(root, did.Key(devicePub), time.Now().Add(time.Hour), "*")
	data, _ := json.Marshal(map[string]any{"proof": proof, "blob": blob})
	asDevice, _ := did.SignAs(device, delegation, SavePurpose, data)
	if _, err := c.call("save", map[string]any{"signed": asDevice}); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("a device replaced the blob: err = %v", err)
	}
	blob["ct"] = "DDDD"
	if saved, err = c.save(root, proof, blob, ""); err != nil || saved["version"] != 2.0 {
		t.Fatalf("saved again = %+v, %v", saved, err)
	}
	if opened, err = c.open(proof, ""); err != nil || opened["blob"].(map[string]any)["ct"] != "DDDD" {
		t.Fatalf("opened after saving again = %+v, %v", opened, err)
	}
}

// start runs a node with the vault on the loopback network, joining through
// bootstrap, and returns it and its address.
func start(t *testing.T, name string, bootstrap []string) (caller, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	url := "wss://" + addr
	key, _ := identity.Load("")
	nw, err := network.New(network.Config{Key: key, ListenPort: l.Addr().(*net.TCPAddr).Port, Announce: []string{url}, Private: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config(t, name)
	cfg.Network.Bootstrap = bootstrap
	n, err := node.New(cfg, key, nw, map[string]module.Factory{Name: New})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		n.Run(ctx)
		close(done)
	}()
	go nw.ListenAndServe(ctx, addr)
	t.Cleanup(func() {
		cancel()
		<-done
	})
	for range 50 { // until it listens
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return caller{t, n}, url
}

// A person's vault may be on another node: their app names it, and the
// node the app is on forwards.
func TestVaultElsewhere(t *testing.T) {
	a, aAddr := start(t, "a", nil)
	b, _ := start(t, "b", []string{aAddr})
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := b.n.Call(context.Background(), "routing.find_node", json.RawMessage(`{"id":"`+a.n.ID+`"}`)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("b never reached a")
		}
	}

	_, root, _ := ed25519.GenerateKey(rand.Reader)
	proof := make([]byte, 32)
	rand.Read(proof)
	blob := map[string]any{"v": 1, "pub": "AAAA", "iv": "BBBB", "ct": "CCCC"}

	// Made through b, in a's vault: a has it, b doesn't, and b forwards.
	saved, err := b.save(root, proof, blob, a.n.ID)
	if err != nil || saved["node"] != a.n.ID {
		t.Fatalf("saved through b in a's vault = %+v, %v", saved, err)
	}
	if opened, err := a.open(proof, ""); err != nil || opened["node"] != a.n.ID || opened["blob"].(map[string]any)["ct"] != "CCCC" {
		t.Fatalf("opened on a = %+v, %v", opened, err)
	}
	if _, err := b.open(proof, ""); module.Code(err) != module.CodeNotFound {
		t.Fatalf("opened on b, which shouldn't have it: err = %v", err)
	}
	if opened, err := b.open(proof, a.n.ID); err != nil || opened["node"] != a.n.ID || opened["blob"].(map[string]any)["ct"] != "CCCC" {
		t.Fatalf("opened through b from a = %+v, %v", opened, err)
	}
	if _, err := b.open(proof, "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("opened from a node that doesn't exist")
	}
}
