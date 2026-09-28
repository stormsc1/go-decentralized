package vault

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-decentralized/did"
	"go-decentralized/internal/identity"
	"go-decentralized/internal/network"
	"go-decentralized/internal/node"
	"go-decentralized/module"
)

func TestVault(t *testing.T) {
	ctx := context.Background()
	cfg, err := node.ParseConfig("test", []byte(`
name: test
network: {mdns: false}
stores: {main: {path: ":memory:"}}
modules:
  - name: vault
    stores: {data: main}
`))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := identity.Load("")
	nw, err := network.New(network.Config{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.New(cfg, key, nw, map[string]module.Factory{Name: New})
	if err != nil {
		t.Fatal(err)
	}
	call := func(name string, in any) (map[string]any, error) {
		t.Helper()
		input, _ := json.Marshal(in)
		result, err := n.Call(ctx, Name+"."+name, input)
		if err != nil {
			return nil, err
		}
		var out map[string]any
		if err := json.Unmarshal(result, &out); err != nil {
			t.Fatal(err)
		}
		return out, nil
	}
	// save has root keep blob under proof, as the browser would.
	save := func(root ed25519.PrivateKey, proof []byte, blob any) (map[string]any, error) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"proof": proof, "blob": blob})
		signed, err := did.Sign(root, SavePurpose, data)
		if err != nil {
			t.Fatal(err)
		}
		return call("save", map[string]any{"signed": signed})
	}

	_, root, _ := ed25519.GenerateKey(rand.Reader)
	proof := make([]byte, 32)
	rand.Read(proof)
	blob := map[string]any{"v": 1, "pub": "AAAA", "iv": "BBBB", "ct": "CCCC"}

	// Nothing until saved; then whoever has the proof opens it.
	if _, err := call("open", map[string]any{"proof": proof}); module.Code(err) != module.CodeNotFound {
		t.Fatalf("opened an account that doesn't exist: err = %v", err)
	}
	saved, err := save(root, proof, blob)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := call("open", map[string]any{"proof": proof})
	if err != nil {
		t.Fatal(err)
	}
	if opened["id"] != saved["id"] || opened["root"] != did.Key(root.Public().(ed25519.PublicKey)) || opened["blob"].(map[string]any)["ct"] != "CCCC" || opened["version"] != 1.0 {
		t.Fatalf("opened %+v", opened)
	}

	// Another proof is another account; a short one isn't a proof; a blob has
	// a size.
	other := make([]byte, 32)
	rand.Read(other)
	if _, err := call("open", map[string]any{"proof": other}); module.Code(err) != module.CodeNotFound {
		t.Fatalf("opened with another proof: err = %v", err)
	}
	if _, err := save(root, proof[:8], blob); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("saved with a short proof: err = %v", err)
	}
	if _, err := save(root, proof, map[string]any{"ct": strings.Repeat("x", 9000)}); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("saved a huge blob: err = %v", err)
	}

	// Only the root replaces its blob: not another root with the proof, and
	// not a device the root authorized.
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := save(stranger, proof, blob); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("a stranger with the proof replaced the blob: err = %v", err)
	}
	devicePub, device, _ := ed25519.GenerateKey(rand.Reader)
	delegation, _ := did.Delegate(root, did.Key(devicePub), time.Now().Add(time.Hour), "*")
	data, _ := json.Marshal(map[string]any{"proof": proof, "blob": blob})
	asDevice, _ := did.SignAs(device, delegation, SavePurpose, data)
	if _, err := call("save", map[string]any{"signed": asDevice}); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("a device replaced the blob: err = %v", err)
	}
	blob["ct"] = "DDDD"
	if saved, err = save(root, proof, blob); err != nil || saved["version"] != 2.0 {
		t.Fatalf("saved again = %+v, %v", saved, err)
	}
	if opened, err = call("open", map[string]any{"proof": proof}); err != nil || opened["blob"].(map[string]any)["ct"] != "DDDD" {
		t.Fatalf("opened after saving again = %+v, %v", opened, err)
	}
}
