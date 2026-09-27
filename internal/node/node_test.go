package node

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

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
    output: {type: object, properties: {caller: {type: string}}}
  - name: add
    input:
      type: object
      required: [a, b]
      properties: {a: {type: integer}, b: {type: integer}}
    output: {type: object, properties: {sum: {type: integer}}}
  - name: node_name
    output: {type: object, properties: {name: {type: string}}}
  - name: note
    description: Keeps a note, in the node's store.
    input: {type: object}
  - name: notes
    description: Lists the notes, newest first.
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
entities:
  - name: note
    schema:
      type: object
      required: [text, time]
      properties: {text: {type: string}, time: {type: integer}}
    indexes: [time]
`))

func newEcho(_ func(any) error, env module.Env) (module.Module, error) { return echo{env}, nil }

func (echo) Manifest() module.Manifest { return echoManifest }

func (e echo) Handlers() map[string]module.Handler {
	return map[string]module.Handler{
		"whoami": module.HandlerFor(func(ctx context.Context, _ struct{}) (map[string]string, error) {
			return map[string]string{"caller": module.Caller(ctx)}, nil
		}),
		"add": module.HandlerFor(func(_ context.Context, in struct{ A, B int }) (map[string]int, error) {
			return map[string]int{"sum": in.A + in.B}, nil
		}),
		"node_name": module.HandlerFor(func(ctx context.Context, _ struct{}) (map[string]string, error) {
			var info module.NodeInfo
			err := e.env.Call(ctx, "node.info", nil, &info)
			return map[string]string{"name": info.Name}, err
		}),
		"note": module.HandlerFor(func(ctx context.Context, in map[string]any) (struct{}, error) {
			id, _ := in["id"].(string)
			delete(in, "id")
			if err := e.env.Entities("note").Put(ctx, id, in); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, e.env.Emit(ctx, "noted", map[string]any{"text": in["text"]})
		}),
		"notes": module.HandlerFor(func(ctx context.Context, _ struct{}) (map[string]any, error) {
			var notes []map[string]any
			err := e.env.Entities("note").Query(ctx, module.Query{OrderBy: "time", Desc: true}, &notes)
			return map[string]any{"notes": notes}, err
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

// start runs a node with one module, listening on localhost, until the test
// ends. It joins the network through bootstrap, if any.
func start(t *testing.T, bootstrap []string, mc ModuleConfig) (*Node, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	key, _ := identity.Load("")
	nw, err := network.New(network.Config{Key: key, ListenPort: l.Addr().(*net.TCPAddr).Port, Announce: []string{addr}, Private: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenSQLite("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n, err := New(Config{Name: "test", Network: NetworkConfig{Bootstrap: bootstrap, MDNS: new(bool)}, Modules: []ModuleConfig{mc}}, key, nw, st, map[string]module.Factory{"echo": newEcho})
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
	return n, addr
}
