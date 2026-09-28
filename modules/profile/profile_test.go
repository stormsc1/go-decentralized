package profile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"testing"
	"time"

	"go-decentralized/did"
	"go-decentralized/internal/identity"
	"go-decentralized/internal/network"
	"go-decentralized/internal/node"
	"go-decentralized/module"
)

// someone is a person with a root and a device the root authorized.
type someone struct {
	id   string
	auth did.Signed
	sign func(t *testing.T, v any) did.Signed
}

func newPerson(t *testing.T) someone {
	t.Helper()
	root, rootKey, _ := did.New()
	device, deviceKey, _ := did.New()
	auth, err := did.Delegate(rootKey, device, time.Now().Add(time.Hour), "*")
	if err != nil {
		t.Fatal(err)
	}
	return someone{id: root, auth: auth, sign: func(t *testing.T, v any) did.Signed {
		t.Helper()
		data, _ := json.Marshal(v)
		s, err := did.SignAs(deviceKey, auth, SetPurpose, data)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}}
}

// tester calls the profile module on a node, as a person signed in or not.
type tester struct {
	t    *testing.T
	n    *node.Node
	user string
}

func (c tester) as(p someone) tester {
	c.user = p.id
	return c
}

func (c tester) call(name string, in any, out any) error {
	c.t.Helper()
	input, _ := json.Marshal(in)
	ctx := context.Background()
	if c.user != "" {
		ctx = module.WithUser(ctx, c.user)
	}
	result, err := c.n.Call(ctx, Name+"."+name, input)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(result, out)
	}
	return nil
}

func (c tester) must(name string, in any, out any) {
	c.t.Helper()
	if err := c.call(name, in, out); err != nil {
		c.t.Fatalf("%s: %v", name, err)
	}
}

func config(t *testing.T, name string) node.Config {
	t.Helper()
	cfg, err := node.ParseConfig(name, []byte(`
name: `+name+`
network: {mdns: false}
stores:
  main: {path: ":memory:"}
  files: {driver: file}
modules:
  - name: profile
    stores: {data: main, avatars: files}
    config: {fresh: 300ms}
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// start runs a node with the profile module on the loopback network.
func start(t *testing.T, name string, bootstrap []string) (tester, string) {
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
	return tester{t: t, n: n}, url
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("never: %s", what)
}

func TestProfile(t *testing.T) {
	key, _ := identity.Load("")
	nw, err := network.New(network.Config{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.New(config(t, "test"), key, nw, map[string]module.Factory{Name: New})
	if err != nil {
		t.Fatal(err)
	}
	c := tester{t: t, n: n}
	alice, bob := newPerson(t), newPerson(t)
	updated, _ := n.Subscribe("profile.updated")
	defer updated.Close()

	// Setting takes signing in as the person whose profile it is.
	profile := map[string]any{"name": "Alice", "bio": "Down the rabbit hole", "time": time.Now()}
	if err := c.call("set", map[string]any{"signed": alice.sign(t, profile)}, nil); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("set without signing in: err = %v", err)
	}
	if err := c.as(bob).call("set", map[string]any{"signed": alice.sign(t, profile)}, nil); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("bob set alice's profile: err = %v", err)
	}
	var p Profile
	c.as(alice).must("set", map[string]any{"signed": alice.sign(t, profile)}, &p)
	if p.ID != alice.id || p.Name != "Alice" || p.Bio != "Down the rabbit hole" || p.Node != n.ID || p.Avatar != "" {
		t.Fatalf("set = %+v", p)
	}
	if e := <-updated.Events(); string(e.Body) != `{"id":"`+alice.id+`"}` {
		t.Fatalf("updated = %s", e.Body)
	}
	if err := c.as(alice).call("set", map[string]any{"signed": alice.sign(t, map[string]any{"name": "", "time": time.Now()})}, nil); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("set an empty name: err = %v", err)
	}
	// Older profiles don't replace newer ones; newer do.
	if err := c.as(alice).call("set", map[string]any{"signed": alice.sign(t, map[string]any{"name": "Old", "time": time.Now().Add(-time.Hour)})}, nil); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("set an older profile: err = %v", err)
	}
	c.as(alice).must("set", map[string]any{"signed": alice.sign(t, map[string]any{"name": "Alice Liddell", "time": time.Now()})}, &p)
	<-updated.Events()
	var got Profile
	c.must("get", map[string]any{"id": alice.id}, &got)
	if got.Name != "Alice Liddell" || got.Bio != "" {
		t.Fatalf("get = %+v", got)
	}
	if err := c.call("get", map[string]any{"id": bob.id}, nil); !notFound(err) {
		t.Fatalf("got a profile nobody set: err = %v", err)
	}

	// Avatars: an image kept by its hash, named in the profile.
	png := []byte("\x89PNG not really")
	sum := sha256.Sum256(png)
	var out struct{ Avatar string }
	if err := c.call("set_avatar", map[string]any{"data": png, "content_type": "image/png"}, &out); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("set an avatar without signing in: err = %v", err)
	}
	c.as(alice).must("set_avatar", map[string]any{"data": png, "content_type": "image/png"}, &out)
	if out.Avatar != hex.EncodeToString(sum[:]) {
		t.Fatalf("avatar key = %s", out.Avatar)
	}
	if err := c.as(alice).call("set", map[string]any{"signed": alice.sign(t, map[string]any{"name": "Alice", "avatar": hex.EncodeToString(sha256.New().Sum(nil)), "time": time.Now()})}, nil); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("named an avatar that isn't there: err = %v", err)
	}
	c.as(alice).must("set", map[string]any{"signed": alice.sign(t, map[string]any{"name": "Alice", "avatar": out.Avatar, "time": time.Now()})}, &p)
	var img struct {
		Key         string
		ContentType string `json:"content_type"`
		Data        []byte
	}
	c.must("avatar", map[string]any{"id": alice.id}, &img)
	if img.Key != out.Avatar || img.ContentType != "image/png" || string(img.Data) != string(png) {
		t.Fatalf("avatar = %+v", img)
	}

	// lookup is get for many, leaving out the unknown.
	var many struct{ Profiles []Profile }
	c.must("lookup", map[string]any{"people": []map[string]string{{"id": alice.id}, {"id": bob.id}}}, &many)
	if len(many.Profiles) != 1 || many.Profiles[0].ID != alice.id {
		t.Fatalf("lookup = %+v", many.Profiles)
	}
}

// A profile set on one node is found from another: through a hint of where
// the person is, or through the DHT, where the home node announces their
// DID. Copies are checked against the person's signature and refreshed.
func TestAcrossNodes(t *testing.T) {
	a, aAddr := start(t, "a", nil)
	b, _ := start(t, "b", []string{aAddr})
	alice := newPerson(t)
	eventually(t, "b can reach a", func() bool {
		_, err := b.n.Call(context.Background(), "routing.find_node", json.RawMessage(`{"id":"`+a.n.ID+`"}`))
		return err == nil
	})
	png := []byte("PNG")
	var out struct{ Avatar string }
	a.as(alice).must("set_avatar", map[string]any{"data": png, "content_type": "image/png"}, &out)
	a.as(alice).must("set", map[string]any{"signed": alice.sign(t, map[string]any{"name": "Alice", "avatar": out.Avatar, "time": time.Now()})}, nil)

	// With a hint of where Alice is, b fetches from a and keeps a copy.
	var p Profile
	b.must("get", map[string]any{"id": alice.id, "node": a.n.ID}, &p)
	if p.Name != "Alice" || p.Node != a.n.ID || p.Fetched.IsZero() {
		t.Fatalf("fetched = %+v", p)
	}
	var img struct {
		ContentType string `json:"content_type"`
	}
	b.must("avatar", map[string]any{"id": alice.id}, &img)
	if img.ContentType != "image/png" {
		t.Fatalf("avatar through b = %+v", img)
	}
	// b's copy is served until it goes stale; then a is asked again, and the
	// newer profile replaces the copy.
	a.as(alice).must("set", map[string]any{"signed": alice.sign(t, map[string]any{"name": "Alice Liddell", "time": time.Now()})}, nil)
	b.must("get", map[string]any{"id": alice.id}, &p)
	if p.Name != "Alice" {
		t.Fatalf("a fresh copy was refetched: %+v", p)
	}
	eventually(t, "b refreshes its copy", func() bool {
		b.must("get", map[string]any{"id": alice.id}, &p)
		return p.Name == "Alice Liddell"
	})

	// Without any hint, a person's DID resolves through the DHT to their home.
	carol := newPerson(t)
	a.as(carol).must("set", map[string]any{"signed": carol.sign(t, map[string]any{"name": "Carol", "time": time.Now()})}, nil)
	eventually(t, "b finds carol's home through the DHT", func() bool {
		return b.call("get", map[string]any{"id": carol.id}, &p) == nil && p.Name == "Carol" && p.Node == a.n.ID
	})
	// Other nodes can't set someone's profile through fetch: it only serves.
	if err := b.call("fetch", map[string]any{"id": alice.id}, nil); !notFound(err) {
		t.Fatalf("b, not alice's home, served her profile: err = %v", err)
	}
}
