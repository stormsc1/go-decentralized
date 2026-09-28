package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go-decentralized/did"
	"go-decentralized/internal/identity"
	"go-decentralized/internal/network"
	"go-decentralized/internal/store"
	"go-decentralized/module"
)

// echo is a test module: it tells callers who they are, adds numbers, asks
// its node for its name, keeps notes and pokes other nodes.
type echo struct{ env module.Env }

var echoManifest = module.MustParseManifest([]byte(`
name: echo
version: 1.0.0
capabilities:
  - name: whoami
    access: network
    output: {type: object, properties: {caller: {type: string}, user: {type: string}}}
  - name: add
    input:
      type: object
      required: [a, b]
      properties: {a: {type: integer}, b: {type: integer}}
    output: {type: object, properties: {sum: {type: integer}}}
  - name: node_name
    output: {type: object, properties: {name: {type: string}}}
  - name: note
    description: Keeps a note, in the node's store, only if its version is as expected when if_version is set.
    input: {type: object}
    output: {type: object, properties: {version: {type: integer}}}
  - name: notes
    description: Lists the notes, newest first.
  - name: add_notes
    description: Adds notes that don't exist yet, all or none.
    input: {type: object, required: [notes], properties: {notes: {type: array}}}
  - name: poke
    access: network
    description: Tells subscribers who poked.
  - name: poke_node
    description: Pokes the node with the given ID, without waiting.
    input: {type: object, required: [id], properties: {id: {type: string}}}
events:
  - name: noted
    schema: {type: object, required: [text], properties: {text: {type: string}}}
  - name: poked
    schema: {type: object, properties: {from: {type: string}}}
stores:
  - name: notes
    type: entity
    entities:
      - name: note
        schema:
          type: object
          required: [text, time]
          properties: {text: {type: string}, time: {type: integer}}
        indexes: [time]
config:
  type: object
  properties: {greeting: {type: string}}
`))

func newEcho(_ func(any) error, env module.Env) (module.Module, error) { return echo{env}, nil }

func (echo) Manifest() module.Manifest { return echoManifest }

func (e echo) Handlers() map[string]module.Handler {
	return map[string]module.Handler{
		"whoami": module.HandlerFor(func(ctx context.Context, _ struct{}) (map[string]string, error) {
			return map[string]string{"caller": module.Caller(ctx), "user": module.User(ctx)}, nil
		}),
		"add": module.HandlerFor(func(_ context.Context, in struct{ A, B int }) (map[string]int, error) {
			return map[string]int{"sum": in.A + in.B}, nil
		}),
		"node_name": module.HandlerFor(func(ctx context.Context, _ struct{}) (map[string]string, error) {
			var info module.NodeInfo
			err := e.env.Call(ctx, "node.info", nil, &info)
			return map[string]string{"name": info.Name}, err
		}),
		"note": module.HandlerFor(func(ctx context.Context, in map[string]any) (map[string]int64, error) {
			id, _ := in["id"].(string)
			delete(in, "id")
			var version int64
			var err error
			if ifVersion, ok := in["if_version"].(float64); ok {
				delete(in, "if_version")
				version, err = e.env.Entities("note").PutIf(ctx, id, in, int64(ifVersion))
			} else {
				version, err = e.env.Entities("note").Put(ctx, id, in)
			}
			if err != nil {
				return nil, err
			}
			if to, ok := in["to"].([]any); ok {
				var people []string
				for _, p := range to {
					people = append(people, p.(string))
				}
				return map[string]int64{"version": version}, e.env.EmitTo(ctx, "noted", map[string]any{"text": in["text"]}, people)
			}
			return map[string]int64{"version": version}, e.env.Emit(ctx, "noted", map[string]any{"text": in["text"]})
		}),
		"notes": module.HandlerFor(func(ctx context.Context, _ struct{}) (map[string]any, error) {
			records, err := e.env.Entities("note").Query(ctx, module.Query{OrderBy: "time", Desc: true})
			notes := []json.RawMessage{}
			for _, r := range records {
				notes = append(notes, r.Data)
			}
			return map[string]any{"notes": notes}, err
		}),
		"add_notes": module.HandlerFor(func(ctx context.Context, in struct{ Notes []map[string]any }) (struct{}, error) {
			b := e.env.Batch()
			for _, note := range in.Notes {
				id, _ := note["id"].(string)
				delete(note, "id")
				b.PutIf("note", id, note, 0)
			}
			_, err := b.Commit(ctx)
			return struct{}{}, err
		}),
		"poke": module.HandlerFor(func(ctx context.Context, _ struct{}) (struct{}, error) {
			return struct{}{}, e.env.Emit(ctx, "poked", map[string]string{"from": module.Caller(ctx)})
		}),
		"poke_node": module.HandlerFor(func(ctx context.Context, in struct{ ID string }) (struct{}, error) {
			return struct{}{}, e.env.NotifyNode(ctx, in.ID, "echo.poke", nil)
		}),
	}
}

// TestMain lets the test binary run echo as a process module, for nodes that
// start it.
func TestMain(m *testing.M) {
	if os.Getenv(module.ProtocolEnv) != "" {
		if err := module.Serve(newEcho); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestNativeModule(t *testing.T) {
	ctx := context.Background()
	caller, _ := start(t, nil, ModuleConfig{Name: "echo"})
	callee, calleeAddr := start(t, nil, ModuleConfig{Name: "echo"})
	testModule(t, ctx, caller, callee, calleeAddr)
}

func TestProcessModule(t *testing.T) {
	ctx := context.Background()
	caller, _ := start(t, nil, ModuleConfig{Name: "echo"})
	callee, calleeAddr := start(t, nil, ModuleConfig{Name: "echo", Run: []string{os.Args[0]}})
	if info := callee.Info(); info.Modules[len(info.Modules)-1].Runtime != "process" {
		t.Fatalf("echo runs as %q", info.Modules[len(info.Modules)-1].Runtime)
	}
	testModule(t, ctx, caller, callee, calleeAddr)
}

// testModule checks that calls reach callee's echo module: from caller over
// the network, and from callee itself.
func testModule(t *testing.T, ctx context.Context, caller, callee *Node, calleeAddr string) {
	t.Helper()
	peer := network.Peer{ID: callee.ID, Addrs: []string{calleeAddr}}
	var out struct {
		Caller string
		Sum    int
		Name   string
	}
	call := func(result json.RawMessage, err error) error {
		out.Caller, out.Sum, out.Name = "", 0, ""
		if err == nil {
			err = json.Unmarshal(result, &out)
		}
		return err
	}

	if err := call(caller.Network.Call(ctx, peer, "echo.whoami", nil)); err != nil || out.Caller != caller.ID {
		t.Fatalf("remote whoami = %q, %v; want the caller %.8s", out.Caller, err, caller.ID)
	}
	if err := call(callee.Call(ctx, "echo.whoami", nil)); err != nil || out.Caller != callee.ID {
		t.Fatalf("local whoami = %q, %v; want the node itself %.8s", out.Caller, err, callee.ID)
	}
	if err := call(caller.Network.Call(ctx, peer, "echo.add", json.RawMessage(`{"a":1,"b":2}`))); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("remote call to a local capability: err = %v", err)
	}
	if err := call(callee.Call(ctx, "echo.add", json.RawMessage(`{"a":"1"}`))); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("call with an input its schema rejects: err = %v", err)
	}
	if err := call(callee.Call(ctx, "echo.add", json.RawMessage(`{"a":1,"b":2}`))); err != nil || out.Sum != 3 {
		t.Fatalf("add = %d, %v", out.Sum, err)
	}
	if err := call(callee.Call(ctx, "echo.node_name", nil)); err != nil || out.Name != "test" {
		t.Fatalf("node_name = %q, %v", out.Name, err)
	}
	if err := call(callee.Call(ctx, "echo.missing", nil)); module.Code(err) != module.CodeUnimplemented {
		t.Fatalf("call to a missing capability: err = %v", err)
	}

	// The module keeps records in its node's store, checked against its
	// entity type's schema; tools can't reach them. It tells subscribers of
	// each.
	noted, err := callee.Subscribe("echo.noted")
	if err != nil {
		t.Fatal(err)
	}
	defer noted.Close()
	for _, note := range []string{`{"id":"a","text":"first","time":1}`, `{"id":"b","text":"second","time":2}`} {
		if _, err := callee.Call(ctx, "echo.note", json.RawMessage(note)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := callee.Call(ctx, "echo.note", json.RawMessage(`{"id":"c","text":3,"time":3}`)); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("storing a record its schema rejects: err = %v", err)
	}
	result, err := callee.Call(ctx, "echo.notes", nil)
	if err != nil || string(result) != `{"notes":[{"text":"second","time":2},{"text":"first","time":1}]}` {
		t.Fatalf("notes = %s, %v", result, err)
	}
	// Writes carry versions, and a write can require one; a batch is all or
	// nothing.
	if result, err := callee.Call(ctx, "echo.note", json.RawMessage(`{"id":"a","text":"first!","time":1,"if_version":1}`)); err != nil || string(result) != `{"version":2}` {
		t.Fatalf("conditional note = %s, %v", result, err)
	}
	if _, err := callee.Call(ctx, "echo.note", json.RawMessage(`{"id":"a","text":"first?","time":1,"if_version":1}`)); module.Code(err) != module.CodeConflict {
		t.Fatalf("stale conditional note: err = %v", err)
	}
	if _, err := callee.Call(ctx, "echo.add_notes", json.RawMessage(`{"notes":[{"id":"z","text":"only","time":9},{"id":"a","text":"clash","time":1}]}`)); module.Code(err) != module.CodeConflict {
		t.Fatalf("batch adding an existing note: err = %v", err)
	}
	// Had the failed batch added z, adding it now would clash too.
	if _, err := callee.Call(ctx, "echo.add_notes", json.RawMessage(`{"notes":[{"id":"z","text":"only","time":9}]}`)); err != nil {
		t.Fatal(err)
	}
	if result, err := callee.Call(ctx, "echo.notes", nil); err != nil || string(result) != `{"notes":[{"text":"only","time":9},{"text":"second","time":2},{"text":"first!","time":1}]}` {
		t.Fatalf("notes after the batch = %s, %v", result, err)
	}
	if _, err := callee.Call(ctx, "store.kv_put", json.RawMessage(`{"key":"k","value":1}`)); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("a tool used a module's store: err = %v", err)
	}
	for _, want := range []string{`{"text":"first"}`, `{"text":"second"}`} {
		if e := next(t, noted); e.Ref != "echo.noted" || string(e.Body) != want {
			t.Fatalf("event %s %s, want echo.noted %s", e.Ref, e.Body, want)
		}
	}

	// The module notifies another node, which doesn't answer.
	poked, err := caller.Subscribe("echo.poked")
	if err != nil {
		t.Fatal(err)
	}
	defer poked.Close()
	if _, err := callee.Call(ctx, "echo.poke_node", json.RawMessage(`{"id":"`+caller.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if e := next(t, poked); string(e.Body) != `{"from":"`+callee.ID+`"}` {
		t.Fatalf("poked by %s, want %.8s", e.Body, callee.ID)
	}
}

// next returns the next event of sub, failing the test if none comes soon.
func next(t *testing.T, sub *Subscription) Event {
	t.Helper()
	select {
	case e, ok := <-sub.Events():
		if !ok {
			t.Fatal("the subscription ended")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return Event{}
}

// Local tools subscribe to events over the local API, and lose their
// subscription if they fall behind.
func TestEvents(t *testing.T) {
	ctx := context.Background()
	n, _ := start(t, nil, ModuleConfig{Name: "echo"})
	if _, err := n.Subscribe("echo.missing"); module.Code(err) != module.CodeNotFound {
		t.Fatalf("subscribed to an event no module has: err = %v", err)
	}
	api := httptest.NewServer(n.Handler())
	defer api.Close()
	res, err := http.Get(api.URL + "/v1/events?ref=echo.noted&ref=echo.poked")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Call(ctx, "echo.note", json.RawMessage(`{"id":"a","text":"hi","time":1}`)); err != nil {
		t.Fatal(err)
	}
	stream := make([]byte, len("event: echo.noted\ndata: {\"text\":\"hi\"}\n\n"))
	if _, err := io.ReadFull(res.Body, stream); err != nil || string(stream) != "event: echo.noted\ndata: {\"text\":\"hi\"}\n\n" {
		t.Fatalf("stream = %q, %v", stream, err)
	}
	res.Body.Close()

	// Events for particular people reach them, and the node's own tools;
	// events for everyone reach everyone.
	alices, _ := n.SubscribeAs("did:key:alice", "echo.noted")
	defer alices.Close()
	bobs, _ := n.SubscribeAs("did:key:bob", "echo.noted")
	defer bobs.Close()
	nobodys, _ := n.SubscribeAs("", "echo.noted")
	defer nobodys.Close()
	all, _ := n.Subscribe("echo.noted")
	defer all.Close()
	if _, err := n.Call(ctx, "echo.note", json.RawMessage(`{"id":"b","text":"for alice","time":2,"to":["did:key:alice"]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Call(ctx, "echo.note", json.RawMessage(`{"id":"c","text":"for everyone","time":3}`)); err != nil {
		t.Fatal(err)
	}
	for _, e := range []Event{next(t, alices), next(t, all)} {
		if string(e.Body) != `{"text":"for alice"}` || len(e.To) != 1 {
			t.Fatalf("alice's event = %+v", e)
		}
	}
	for _, s := range []*Subscription{alices, bobs, nobodys, all} {
		if e := next(t, s); string(e.Body) != `{"text":"for everyone"}` {
			t.Fatalf("everyone's event = %+v", e)
		}
	}

	sub, err := n.Subscribe("echo.noted")
	if err != nil {
		t.Fatal(err)
	}
	for range behind + 1 {
		if _, err := n.Call(ctx, "echo.note", json.RawMessage(`{"id":"a","text":"hi","time":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	got := 0
	for range sub.Events() {
		got++
	}
	if got != behind {
		t.Fatalf("got %d events before the subscription ended, want %d", got, behind)
	}
}

// A node calling another by ID alone finds it through routing: here, over the
// DHT, knowing only the other's address to bootstrap from.
func TestCallByID(t *testing.T) {
	callee, calleeAddr := start(t, nil, ModuleConfig{Name: "echo"})
	caller, _ := start(t, []string{calleeAddr}, ModuleConfig{Name: "echo"})
	result, err := caller.callNode(context.Background(), callee.ID, "echo.whoami", nil)
	var out struct{ Caller string }
	if err == nil {
		err = json.Unmarshal(result, &out)
	}
	if err != nil || out.Caller != caller.ID {
		t.Fatalf("whoami = %q, %v; want the caller %.8s", out.Caller, err, caller.ID)
	}
}

// A node binds the stores modules declare to its own: to the only one it
// has for modules, or as the node definition says.
func TestStores(t *testing.T) {
	ctx := context.Background()
	cfg := func(stores map[string]store.Config, bindings map[string]string) Config {
		return Config{Name: "test", Network: NetworkConfig{MDNS: new(bool)}, Stores: stores, Modules: []ModuleConfig{{Name: "echo", Stores: bindings}}}
	}
	memory := store.Config{Options: store.Options{"path": ":memory:"}}
	one := map[string]store.Config{"a": memory}
	two := map[string]store.Config{"a": memory, "b": memory}
	for _, bad := range []struct {
		cfg  Config
		want string
	}{
		{cfg(one, nil), "isn't bound"}, // even with one store: bindings are explicit
		{cfg(two, map[string]string{"notes": "c"}), "doesn't have"},
		{cfg(one, map[string]string{"notes": "local"}), "the node's own"},
		{cfg(one, map[string]string{"notes": "a", "other": "a"}), "doesn't declare"},
	} {
		if _, err := newNode(t, bad.cfg); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("New with %+v: err = %v, want %q", bad.cfg.Modules[0].Stores, err, bad.want)
		}
	}
	n, err := newNode(t, cfg(two, map[string]string{"notes": "b"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Call(ctx, "echo.note", json.RawMessage(`{"id":"a","text":"bound","time":1}`)); err != nil {
		t.Fatal(err)
	}
	// The node's own parts keep their state in the local store.
	if err := n.local("test").Put(ctx, "k", 1); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := n.local("test").Get(ctx, "k", &v); err != nil || v != 1 {
		t.Fatalf("local Get = %d, %v", v, err)
	}
}

// A person signs in to the local API by signing a challenge with a device
// key their root delegated to; calls then know who they're for.
func TestSignIn(t *testing.T) {
	n, _ := start(t, nil, ModuleConfig{Name: "echo"})
	api := httptest.NewServer(n.Handler())
	defer api.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	root, rootKey, _ := did.New()
	device, deviceKey, _ := did.New()
	authorization, err := did.Delegate(rootKey, device, time.Now().Add(time.Hour), "*")
	if err != nil {
		t.Fatal(err)
	}
	post := func(path string, v any) (*http.Response, []byte) {
		t.Helper()
		body, _ := json.Marshal(v)
		res, err := client.Post(api.URL+path, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res, out
	}
	call := func(ref string, in any) (*http.Response, []byte) { return post("/v1/capabilities/"+ref, in) }

	// Not signed in: capabilities run as nobody.
	if _, out := call("echo.whoami", nil); !strings.Contains(string(out), `"user":""`) {
		t.Fatalf("whoami without a session = %s", out)
	}
	if res, _ := client.Get(api.URL + "/v1/session"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session before signing in: %s", res.Status)
	}

	// Signing in: a challenge, signed for this node by the device.
	res, err := client.Get(api.URL + "/v1/challenge")
	if err != nil {
		t.Fatal(err)
	}
	var challenge struct{ Challenge, Node string }
	if err := json.NewDecoder(res.Body).Decode(&challenge); err != nil || challenge.Node != n.ID {
		t.Fatalf("challenge = %+v, %v", challenge, err)
	}
	sign := func(key ed25519.PrivateKey, purpose string, v any) did.Signed {
		data, _ := json.Marshal(v)
		s, err := did.SignAs(key, authorization, purpose, data)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	claim := map[string]string{"challenge": challenge.Challenge, "node": n.ID}
	if res, _ := post("/v1/login", map[string]any{"signed": sign(deviceKey, "node.other", claim)}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("signed in with a signature for something else: %s", res.Status)
	}
	// The failed try used the challenge up.
	if res, _ := post("/v1/login", map[string]any{"signed": sign(deviceKey, LoginPurpose, claim)}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("signed in with a used challenge: %s", res.Status)
	}
	res, _ = client.Get(api.URL + "/v1/challenge")
	_ = json.NewDecoder(res.Body).Decode(&challenge)
	claim["challenge"] = challenge.Challenge
	res, out := post("/v1/login", map[string]any{"signed": sign(deviceKey, LoginPurpose, claim)})
	if res.StatusCode != http.StatusOK || !strings.Contains(string(out), root) {
		t.Fatalf("login = %s %s", res.Status, out)
	}

	// Signed in: calls are for the person, the root, not the device, and
	// the event stream carries what's for them.
	if _, out := call("echo.whoami", nil); !strings.Contains(string(out), `"user":"`+root+`"`) {
		t.Fatalf("whoami with a session = %s", out)
	}
	stream, err := client.Get(api.URL + "/v1/events?ref=echo.noted")
	if err != nil {
		t.Fatal(err)
	}
	call("echo.note", map[string]any{"id": "x", "text": "not yours", "time": 1, "to": []string{device}})
	call("echo.note", map[string]any{"id": "y", "text": "yours", "time": 2, "to": []string{root}})
	line := make([]byte, len("event: echo.noted\ndata: {\"text\":\"yours\"}\n\n"))
	if _, err := io.ReadFull(stream.Body, line); err != nil || string(line) != "event: echo.noted\ndata: {\"text\":\"yours\"}\n\n" {
		t.Fatalf("the person's stream = %q, %v", line, err)
	}
	stream.Body.Close()
	if res, out := post("/v1/logout", nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %s %s", res.Status, out)
	}
	if _, out := call("echo.whoami", nil); !strings.Contains(string(out), `"user":""`) {
		t.Fatalf("whoami after logout = %s", out)
	}
}

// A module's manifest may give its block in the node definition a schema,
// which the node checks the block against before serving the module. The
// block is the module's environment: e.g. a connection string for tables of
// its own.
func TestConfigSchema(t *testing.T) {
	load := func(config string) error {
		cfg, err := ParseConfig("test", []byte(`
name: test
network: {mdns: false}
stores:
  main: {path: ":memory:"}
modules:
  - name: echo
    stores: {notes: main}
    config: `+config+`
`))
		if err != nil {
			t.Fatal(err)
		}
		_, err = newNode(t, cfg)
		return err
	}
	if err := load(`{greeting: 1}`); err == nil || !strings.Contains(err.Error(), "config") {
		t.Fatalf("started with a block its schema rejects: err = %v", err)
	}
	if err := load(`{greeting: hi}`); err != nil {
		t.Fatal(err)
	}
}

// newNode builds a node from cfg in client mode, without running it.
func newNode(t *testing.T, cfg Config) (*Node, error) {
	t.Helper()
	key, _ := identity.Load("")
	nw, err := network.New(network.Config{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(cfg, key, nw, map[string]module.Factory{"echo": newEcho})
	if n != nil {
		t.Cleanup(n.close)
	}
	return n, err
}

// A node remembers the peers it knew, in its local store, and rejoins
// through them without a bootstrap address.
func TestRemembersPeers(t *testing.T) {
	ctx := context.Background()
	a, aAddr := start(t, nil, ModuleConfig{Name: "echo"})
	dir := t.TempDir()
	onDisk := func(cfg *Config) { cfg.DataDir = dir }
	b, _, stopB := launch(t, []string{aAddr}, ModuleConfig{Name: "echo"}, onDisk)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var peers []struct{ ID string }
		if err := b.local("routing").Get(ctx, "peers", &peers); err == nil && len(peers) == 1 && peers[0].ID == a.ID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("b never remembered a: %v", peers)
		}
	}
	stopB()

	// A node in b's place, with its data but no bootstrap address, finds a.
	c, _ := start(t, nil, ModuleConfig{Name: "echo"}, onDisk)
	result, err := c.callNode(ctx, a.ID, "echo.whoami", nil)
	var out struct{ Caller string }
	if err == nil {
		err = json.Unmarshal(result, &out)
	}
	if err != nil || out.Caller != c.ID {
		t.Fatalf("whoami = %q, %v; want the caller %.8s", out.Caller, err, c.ID)
	}
}

// start runs a node with one module, listening on localhost, until the test
// ends. It joins the network through bootstrap, if any. opts change the
// node definition first.
func start(t *testing.T, bootstrap []string, mc ModuleConfig, opts ...func(*Config)) (*Node, string) {
	t.Helper()
	n, addr, _ := launch(t, bootstrap, mc, opts...)
	return n, addr
}

// launch is start, also returning a function that stops the node early.
func launch(t *testing.T, bootstrap []string, mc ModuleConfig, opts ...func(*Config)) (*Node, string, func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	key, _ := identity.Load("")
	url := "wss://" + addr
	nw, err := network.New(network.Config{Key: key, ListenPort: l.Addr().(*net.TCPAddr).Port, Announce: []string{url}, Private: true})
	if err != nil {
		t.Fatal(err)
	}
	if mc.Stores == nil {
		mc.Stores = map[string]string{"notes": "main"} // echo's store
	}
	cfg := Config{
		Name:    "test",
		Network: NetworkConfig{Bootstrap: bootstrap, MDNS: new(bool)},
		Stores:  map[string]store.Config{"main": {Options: store.Options{"path": ":memory:"}}},
		Modules: []ModuleConfig{mc},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	n, err := New(cfg, key, nw, map[string]module.Factory{"echo": newEcho})
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
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	for range 50 { // until it listens
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return n, url, stop
}
