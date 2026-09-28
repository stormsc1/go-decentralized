package chat

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"go-decentralized/did"
	"go-decentralized/internal/identity"
	"go-decentralized/internal/network"
	"go-decentralized/internal/node"
	"go-decentralized/module"
	"go-decentralized/modules/chat/graph"
)

// person is someone with a root did:key and a device key their root
// authorized, with which they sign what they do.
type person struct {
	id     string // the root's DID: who they are
	device ed25519.PrivateKey
	auth   did.Signed // the root's authorization of the device
}

func newPerson(t *testing.T) person {
	t.Helper()
	root, rootKey, err := did.New()
	if err != nil {
		t.Fatal(err)
	}
	device, deviceKey, err := did.New()
	if err != nil {
		t.Fatal(err)
	}
	auth, err := did.Delegate(rootKey, device, time.Now().Add(time.Hour), "*")
	if err != nil {
		t.Fatal(err)
	}
	return person{root, deviceKey, auth}
}

// sign signs v for purpose, as the person, from their device.
func (p person) sign(t *testing.T, purpose string, v any) did.Signed {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	s, err := did.SignAs(p.device, p.auth, purpose, data)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// event signs an event by the person, on a channel with the given heads.
func (p person) event(t *testing.T, channel string, heads []string, kind string, body any) did.Signed {
	t.Helper()
	data, _ := json.Marshal(body)
	return p.sign(t, graph.Purpose, graph.Event{Channel: channel, Author: p.id, Kind: kind, Body: data, Parents: heads, Time: time.Now().UTC()})
}

// create signs a create event by the person, with members on their nodes.
func (p person) create(t *testing.T, name, kind string, members map[string]string) did.Signed {
	t.Helper()
	var ms []member
	for user, node := range members {
		ms = append(ms, member{User: user, Node: node})
	}
	body, _ := json.Marshal(map[string]any{"name": name, "kind": kind, "members": ms})
	return p.sign(t, graph.Purpose, graph.Event{Author: p.id, Kind: "create", Body: body, Time: time.Now().UTC()})
}

// tester drives the chat on a node, as the person signed in, if any.
type tester struct {
	t    *testing.T
	n    *node.Node
	user string
}

// as returns the tester with the person signed in.
func (c tester) as(p person) tester {
	c.user = p.id
	return c
}

// call calls a chat capability and decodes the result, or returns the error.
func (c tester) call(name string, in any, out any) error {
	c.t.Helper()
	input, err := json.Marshal(in)
	if err != nil {
		c.t.Fatal(err)
	}
	ctx := context.Background()
	if c.user != "" {
		ctx = module.WithUser(ctx, c.user)
	}
	result, err := c.n.Call(ctx, Name+"."+name, input)
	if err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(result, out); err != nil {
			c.t.Fatalf("%s answered %s: %v", name, result, err)
		}
	}
	return nil
}

// must calls, failing the test on error.
func (c tester) must(name string, in any, out any) {
	c.t.Helper()
	if err := c.call(name, in, out); err != nil {
		c.t.Fatalf("%s: %v", name, err)
	}
}

func (c tester) register(p person, name string) User {
	c.t.Helper()
	var u User
	c.must("register", map[string]any{"signed": p.sign(c.t, UserPurpose, map[string]any{"name": name, "time": time.Now()})}, &u)
	return u
}

func (c tester) submit(s did.Signed) (Event, error) {
	c.t.Helper()
	var e Event
	err := c.call("submit", map[string]any{"signed": s}, &e)
	return e, err
}

// channel returns the one channel the person signed in is in.
func (c tester) channel() Channel {
	c.t.Helper()
	var out struct{ Channels []Channel }
	c.must("channels", nil, &out)
	if len(out.Channels) != 1 {
		c.t.Fatalf("%s is in %d channels, want 1", c.user, len(out.Channels))
	}
	return out.Channels[0]
}

func (c tester) history(channel string) []Event {
	c.t.Helper()
	var out struct{ Events []Event }
	c.must("history", map[string]string{"channel": channel}, &out)
	return out.Events
}

// has reports whether the node has the event in the channel.
func (c tester) has(channel, event string) bool {
	var out struct{ Events []Event }
	if c.call("history", map[string]string{"channel": channel}, &out) != nil {
		return false
	}
	return slices.ContainsFunc(out.Events, func(e Event) bool { return e.ID == event })
}

func kinds(events []Event) string {
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return strings.Join(out, ",")
}

// config is a node definition running the chat on an in-memory store,
// catching up with other nodes often.
func config(t *testing.T, name string) node.Config {
	t.Helper()
	cfg, err := node.ParseConfig(name, []byte(`
name: `+name+`
network: {mdns: false}
stores: {main: {path: ":memory:"}}
modules:
  - name: chat
    stores: {data: main}
    config: {sync: 300ms}
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// local runs the chat on a node without a network.
func local(t *testing.T) tester {
	t.Helper()
	key, _ := identity.Load("")
	nw, err := network.New(network.Config{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.New(config(t, "test"), key, nw, map[string]module.Factory{Name: New})
	if err != nil {
		t.Fatal(err)
	}
	return tester{t: t, n: n}
}

func TestChat(t *testing.T) {
	c := local(t)
	self := c.n.ID
	posted, err := c.n.Subscribe("chat.posted")
	if err != nil {
		t.Fatal(err)
	}
	defer posted.Close()
	count := func(want int) {
		t.Helper()
		for range want {
			select {
			case <-posted.Events():
			case <-time.After(5 * time.Second):
				t.Fatal("too few posted events")
			}
		}
	}

	// People are their root DIDs, signing from a device their root
	// authorized; they sign their names, and renaming keeps them.
	alice, bob, carol := newPerson(t), newPerson(t), newPerson(t)
	if u := c.register(alice, "Alice"); u.ID != alice.id || u.Node != self {
		t.Fatalf("registered %+v", u)
	}
	if u := c.register(alice, "Alice Liddell"); u.Name != "Alice Liddell" {
		t.Fatalf("renamed %+v", u)
	}
	c.register(bob, "Bob")
	if err := c.call("register", map[string]any{"signed": bob.sign(t, "chat.other", map[string]any{"name": "x"})}, nil); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("registered with a signature for something else: err = %v", err)
	}

	// Reads are for people signed in.
	if err := c.call("channels", nil, nil); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("listed channels without signing in: err = %v", err)
	}

	// Alice makes a channel with Bob; the create event is the channel.
	created, err := c.submit(alice.create(t, "general", "", map[string]string{alice.id: self, bob.id: self}))
	if err != nil {
		t.Fatal(err)
	}
	ch := c.as(bob).channel()
	if ch.ID != created.ID || ch.Kind != "channel" || len(ch.Members) != 2 || len(ch.Heads) != 1 || ch.Heads[0] != ch.ID {
		t.Fatalf("channel = %+v", ch)
	}
	count(1)

	// Events are for the channel's members: Bob's app gets them, Carol's
	// and one nobody signed in to don't.
	bobs, _ := c.n.SubscribeAs(bob.id, "chat.posted")
	defer bobs.Close()
	carols, _ := c.n.SubscribeAs(carol.id, "chat.posted")
	defer carols.Close()
	nobodys, _ := c.n.SubscribeAs("", "chat.posted")
	defer nobodys.Close()

	// Messages chain: each names the heads as parents and becomes the head.
	m1, err := c.submit(alice.event(t, ch.ID, ch.Heads, "message", map[string]string{"text": "hello"}))
	if err != nil {
		t.Fatal(err)
	}
	m2, err := c.submit(bob.event(t, ch.ID, []string{m1.ID}, "message", map[string]string{"text": "hi"}))
	if err != nil {
		t.Fatal(err)
	}
	if ch = c.as(alice).channel(); len(ch.Heads) != 1 || ch.Heads[0] != m2.ID {
		t.Fatalf("heads = %v, want the last message", ch.Heads)
	}
	count(2)
	for _, want := range []string{m1.ID, m2.ID} {
		select {
		case e := <-bobs.Events():
			if !strings.Contains(string(e.Body), want) {
				t.Fatalf("bob got %s, want %.8s", e.Body, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("bob, a member, got no posted event")
		}
	}
	select {
	case e := <-carols.Events():
		t.Fatalf("carol, not a member, got %s", e.Body)
	case e := <-nobodys.Events():
		t.Fatalf("a stream nobody signed in to got %s", e.Body)
	default:
	}

	// Nobody can post as someone else, tamper, post from outside, edit
	// another's message, or read a channel they aren't in.
	forged := bob.sign(t, graph.Purpose, graph.Event{Channel: ch.ID, Author: alice.id, Kind: "message", Body: json.RawMessage(`{"text":"I am Alice"}`), Parents: ch.Heads, Time: time.Now()})
	if _, err := c.submit(forged); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("forged event: err = %v", err)
	}
	tampered := alice.event(t, ch.ID, ch.Heads, "message", map[string]string{"text": "fine"})
	tampered.Data = []byte(strings.Replace(string(tampered.Data), "fine", "evil", 1))
	if _, err := c.submit(tampered); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("tampered event: err = %v", err)
	}
	if _, err := c.submit(carol.event(t, ch.ID, ch.Heads, "message", map[string]string{"text": "psst"})); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("outsider posted: err = %v", err)
	}
	if _, err := c.submit(bob.event(t, ch.ID, ch.Heads, "edit", map[string]string{"event": m1.ID, "text": "nope"})); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("bob edited alice's message: err = %v", err)
	}
	if err := c.as(carol).call("history", map[string]string{"channel": ch.ID}, nil); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("outsider read the channel: err = %v", err)
	}

	// Edits, reactions, receipts; only messages get edited or reacted to.
	edit, err := c.submit(alice.event(t, ch.ID, ch.Heads, "edit", map[string]string{"event": m1.ID, "text": "hello!"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.submit(bob.event(t, ch.ID, []string{edit.ID}, "reaction", map[string]string{"event": m1.ID, "emoji": "👋"})); err != nil {
		t.Fatal(err)
	}
	if _, err := c.submit(bob.event(t, ch.ID, c.as(bob).channel().Heads, "reaction", map[string]string{"event": edit.ID, "emoji": "x"})); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("reacted to an edit: err = %v", err)
	}
	receipt, err := c.submit(bob.event(t, ch.ID, c.as(bob).channel().Heads, "receipt", map[string]string{"event": m2.ID}))
	if err != nil {
		t.Fatal(err)
	}
	count(3)
	if got := kinds(c.as(alice).history(ch.ID)); got != "create,message,message,edit,reaction,receipt" {
		t.Fatalf("history = %s", got)
	}
	var last struct{ Events []Event }
	c.as(alice).must("history", map[string]any{"channel": ch.ID, "limit": 2}, &last)
	if len(last.Events) != 2 || last.Events[1].ID != receipt.ID {
		t.Fatalf("last two = %+v", last.Events)
	}

	// Anyone in a channel invites; the invitee then sees it. Invites carry
	// the invitee's node.
	c.register(carol, "Carol")
	if _, err := c.submit(bob.event(t, ch.ID, c.as(bob).channel().Heads, "invite", map[string]string{"user": carol.id, "node": self})); err != nil {
		t.Fatal(err)
	}
	if ch = c.as(carol).channel(); len(ch.Members) != 3 {
		t.Fatalf("after the invite, members = %+v", ch.Members)
	}
	if _, err := c.submit(alice.event(t, ch.ID, ch.Heads, "invite", map[string]string{"user": carol.id, "node": self})); module.Code(err) != module.CodeInvalidArgument {
		t.Fatalf("invited again: err = %v", err)
	}
	// Submitting the same event twice is fine, and changes nothing.
	again := alice.event(t, ch.ID, ch.Heads, "message", map[string]string{"text": "once"})
	if _, err := c.submit(again); err != nil {
		t.Fatal(err)
	}
	if _, err := c.submit(again); err != nil {
		t.Fatal(err)
	}
	if n := len(c.as(carol).history(ch.ID)); n != 8 {
		t.Fatalf("history has %d events, want 8", n)
	}

	// Typing is an event, never stored.
	typing, err := c.n.Subscribe("chat.typing")
	if err != nil {
		t.Fatal(err)
	}
	defer typing.Close()
	c.as(carol).must("start_typing", map[string]string{"channel": ch.ID}, nil)
	select {
	case e := <-typing.Events():
		if !strings.Contains(string(e.Body), carol.id) {
			t.Fatalf("typing %s", e.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no typing event")
	}

	// A direct message is a channel of two.
	dm, err := c.submit(alice.create(t, "", "dm", map[string]string{alice.id: self, carol.id: self}))
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Channels []Channel }
	c.as(carol).must("channels", nil, &out)
	if len(out.Channels) != 2 || out.Channels[1].ID != dm.ID || out.Channels[1].Kind != "dm" {
		t.Fatalf("carol's channels = %+v", out.Channels)
	}
}

// start runs the chat on a node with the given key (a new one if nil) that
// listens on localhost until the test ends, joining the network through
// bootstrap, if any.
func start(t *testing.T, name string, bootstrap []string, key ed25519.PrivateKey) (tester, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	url := "wss://" + addr
	if key == nil {
		key, _ = identity.Load("")
	}
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

// eventually polls f until it returns true.
func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("never: %s", what)
}

// A channel across two nodes: each keeps a full copy, events are pushed
// between them, and both order the channel the same way.
func TestAcrossNodes(t *testing.T) {
	a, aAddr := start(t, "a", nil, nil)
	b, _ := start(t, "b", []string{aAddr}, nil)
	alice, bob := newPerson(t), newPerson(t)
	a.register(alice, "Alice")
	b.register(bob, "Bob")
	eventually(t, "a can reach b", func() bool {
		_, err := a.n.Call(context.Background(), "routing.find_node", json.RawMessage(`{"id":"`+b.n.ID+`"}`))
		return err == nil
	})

	// Alice, on a, makes a channel with Bob, who is on b. a asks b Bob's
	// name and pushes the channel to b, which copies it.
	created, err := a.submit(alice.create(t, "cross", "", map[string]string{alice.id: a.n.ID, bob.id: b.n.ID}))
	if err != nil {
		t.Fatal(err)
	}
	var known struct{ Users []User }
	a.must("users", map[string]any{"ids": []string{bob.id}}, &known)
	if len(known.Users) != 1 || known.Users[0].Name != "Bob" || known.Users[0].Node != b.n.ID {
		t.Fatalf("a knows bob as %+v", known.Users)
	}
	eventually(t, "b has the channel", func() bool {
		var out struct{ Channels []Channel }
		return b.as(bob).call("channels", nil, &out) == nil && len(out.Channels) == 1
	})
	ch := b.as(bob).channel()
	if ch.ID != created.ID || len(ch.Members) != 2 {
		t.Fatalf("b's copy = %+v", ch)
	}

	// Bob posts on b; a gets it, and Alice's client hears of it.
	posted, err := a.n.Subscribe("chat.posted")
	if err != nil {
		t.Fatal(err)
	}
	defer posted.Close()
	m1, err := b.submit(bob.event(t, ch.ID, ch.Heads, "message", map[string]string{"text": "hi from b"}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-posted.Events():
		var got Event
		if json.Unmarshal(e.Body, &got) != nil || got.ID != m1.ID {
			t.Fatalf("a's clients heard %s", e.Body)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("a never got bob's message")
	}

	// Both post at once: each side's copy gets two heads, the next event
	// merges them, and both nodes end up with the same order.
	heads := a.as(alice).channel().Heads
	ma, err := a.submit(alice.event(t, ch.ID, heads, "message", map[string]string{"text": "from a"}))
	if err != nil {
		t.Fatal(err)
	}
	mb, err := b.submit(bob.event(t, ch.ID, heads, "message", map[string]string{"text": "from b"}))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "both nodes have both messages", func() bool {
		return len(a.as(alice).history(ch.ID)) == 4 && len(b.as(bob).history(ch.ID)) == 4
	})
	if hs := a.as(alice).channel().Heads; len(hs) != 2 {
		t.Fatalf("a's heads = %v, want the two concurrent messages", hs)
	}
	merge, err := a.submit(alice.event(t, ch.ID, a.as(alice).channel().Heads, "receipt", map[string]string{"event": mb.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if len(merge.Parents) != 2 {
		t.Fatalf("the merge names %v, want both heads", merge.Parents)
	}
	eventually(t, "b has the merge", func() bool { return len(b.as(bob).history(ch.ID)) == 5 })
	ids := func(events []Event) string {
		var out []string
		for _, e := range events {
			out = append(out, e.ID[:8])
		}
		return strings.Join(out, ",")
	}
	if ha, hb := ids(a.as(alice).history(ch.ID)), ids(b.as(bob).history(ch.ID)); ha != hb {
		t.Fatalf("the nodes order the channel differently:\na: %s\nb: %s", ha, hb)
	}
	if hs := a.as(alice).channel().Heads; len(hs) != 1 || hs[0] != merge.ID {
		t.Fatalf("after the merge, a's heads = %v", hs)
	}
	if got := b.as(bob).history(ch.ID); got[2].ID != ma.ID && got[3].ID != ma.ID {
		t.Fatalf("b's history lacks alice's concurrent message: %s", ids(got))
	}

	// Only other members' nodes fetch a channel; a local tool can't.
	if _, err := b.n.Call(context.Background(), "chat.fetch", json.RawMessage(`{"channel":"`+ch.ID+`"}`)); module.Code(err) != module.CodePermissionDenied {
		t.Fatalf("a local tool fetched: err = %v", err)
	}

	// Typing and presence cross nodes: Alice types on a, Bob's client on b
	// hears; Alice's client heartbeats on a, and b knows she's online.
	typingOnB, err := b.n.Subscribe("chat.typing")
	if err != nil {
		t.Fatal(err)
	}
	defer typingOnB.Close()
	presenceOnB, err := b.n.Subscribe("chat.presence")
	if err != nil {
		t.Fatal(err)
	}
	defer presenceOnB.Close()
	a.as(alice).must("start_typing", map[string]string{"channel": ch.ID}, nil)
	select {
	case e := <-typingOnB.Events():
		if !strings.Contains(string(e.Body), alice.id) {
			t.Fatalf("b's clients heard typing %s", e.Body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("b never heard alice typing")
	}
	a.as(alice).must("heartbeat", nil, nil)
	select {
	case e := <-presenceOnB.Events():
		if !strings.Contains(string(e.Body), alice.id) || !strings.Contains(string(e.Body), "true") {
			t.Fatalf("b's clients heard presence %s", e.Body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("b never heard alice come online")
	}
	var online struct{ Online []string }
	b.as(bob).must("online", map[string]any{"users": []string{alice.id, bob.id}}, &online)
	if len(online.Online) != 1 || online.Online[0] != alice.id {
		t.Fatalf("b sees online %v, want alice", online.Online)
	}
	if err := b.as(alice).call("heartbeat", nil, nil); module.Code(err) != module.CodeNotFound {
		t.Fatalf("b took a heartbeat for someone registered on a: err = %v", err)
	}
}

// A node that missed a channel, or events, catches up: nodes compare heads
// every so often and fetch what they lack.
func TestCatchUp(t *testing.T) {
	a, aAddr := start(t, "a", nil, nil)
	bKey, _ := identity.Load("")
	bID := module.NodeID(bKey.Public().(ed25519.PublicKey))
	alice, bob := newPerson(t), newPerson(t)
	a.register(alice, "Alice")

	// Alice makes a channel with Bob, whose node isn't running yet: the push
	// fails, and Bob is known by a placeholder.
	created, err := a.submit(alice.create(t, "later", "", map[string]string{alice.id: a.n.ID, bob.id: bID}))
	if err != nil {
		t.Fatal(err)
	}
	m1, err := a.submit(alice.event(t, created.ID, []string{created.ID}, "message", map[string]string{"text": "anyone there?"}))
	if err != nil {
		t.Fatal(err)
	}

	// Bob's node comes up and joins; a's sync brings it the channel, and it
	// learns Bob's name. Then it stays in step through a's later events.
	b, _ := start(t, "b", []string{aAddr}, bKey)
	b.register(bob, "Bob")
	eventually(t, "b caught up", func() bool { return b.as(bob).has(created.ID, m1.ID) })
	if got := kinds(b.as(bob).history(created.ID)); got != "create,message" {
		t.Fatalf("b's copy = %s", got)
	}
	var known struct{ Users []User }
	eventually(t, "a learned bob's name", func() bool {
		return a.call("users", map[string]any{"ids": []string{bob.id}}, &known) == nil && len(known.Users) == 1 && known.Users[0].Name == "Bob"
	})
	m2, err := b.submit(bob.event(t, created.ID, b.as(bob).channel().Heads, "message", map[string]string{"text": "here now"}))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "a has bob's message", func() bool { return a.as(alice).has(created.ID, m2.ID) })
}
