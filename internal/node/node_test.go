package node

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"go-decentralized/internal/identity"
	"go-decentralized/internal/network"
	"go-decentralized/internal/store"
	"go-decentralized/module"
)

// echo is a test module: it tells callers who they are, adds numbers, and
// asks its node for its name.
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
			return struct{}{}, e.env.Entities("note").Put(ctx, id, in)
		}),
		"notes": module.HandlerFor(func(ctx context.Context, _ struct{}) (map[string]any, error) {
			var notes []map[string]any
			err := e.env.Entities("note").Query(ctx, module.Query{OrderBy: "time", Desc: true}, &notes)
			return map[string]any{"notes": notes}, err
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
	// entity type's schema; tools can't reach them.
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
