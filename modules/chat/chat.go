// Package chat is a realtime chat across nodes: channels and direct
// messages, with edits, reactions, read receipts, typing and presence.
// People are their did:key and sign every event; nodes only verify, store,
// tell their clients and push to the other members' nodes, each of which
// keeps a full copy of every channel one of its people is in, and catches up
// with the others by comparing heads. Every change to a channel is an event
// in its graph (package graph), so every node orders a channel the same way.
// See docs/design/identity-storage-chat.md.
package chat

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"go-decentralized/did"
	"go-decentralized/module"
	"go-decentralized/modules/chat/graph"
)

const Name = "chat"

//go:embed module.yaml
var manifest []byte

// UserPurpose is what people sign their registration for.
const UserPurpose = "chat.user"

const (
	defaultHistory = 100              // how many events history returns unless asked
	defaultSync    = time.Minute      // how often nodes catch up with each other
	onlineFor      = 90 * time.Second // how long a heartbeat keeps someone online
	retries        = 3                // how often a write is retried when another happened first
	pushTimeout    = 30 * time.Second // bounds a call to another node
	fetchRounds    = 100              // bounds how deep a fetch chases parents
)

// Config is the chat's block in the node definition.
type Config struct {
	// Sync is how often the node catches up with other members' nodes.
	Sync time.Duration `yaml:"sync"`
}

type Module struct {
	env module.Env
	cfg Config

	mu     sync.Mutex
	online map[string]time.Time // people online, by DID, when last seen
}

func New(decode func(any) error, env module.Env) (module.Module, error) {
	m := &Module{env: env, online: map[string]time.Time{}}
	if err := decode(&m.cfg); err != nil {
		return nil, err
	}
	if m.cfg.Sync <= 0 {
		m.cfg.Sync = defaultSync
	}
	return m, nil
}

func (m *Module) Manifest() module.Manifest { return module.MustParseManifest(manifest) }

func (m *Module) Handlers() map[string]module.Handler {
	return map[string]module.Handler{
		"register":     module.HandlerFor(m.register),
		"users":        module.HandlerFor(m.users),
		"user_info":    module.HandlerFor(m.userInfo),
		"submit":       module.HandlerFor(m.submit),
		"channels":     module.HandlerFor(m.channels),
		"history":      module.HandlerFor(m.history),
		"start_typing": module.HandlerFor(m.typing),
		"heartbeat":    module.HandlerFor(m.heartbeat),
		"online":       module.HandlerFor(m.whoIsOnline),
		"push":         module.HandlerFor(m.push),
		"fetch":        module.HandlerFor(m.fetch),
		"sync":         module.HandlerFor(m.sync),
		"notice":       module.HandlerFor(m.notice),
	}
}

// Run catches up with other members' nodes every Config.Sync, and lets
// people go offline when their heartbeats stop.
func (m *Module) Run(ctx context.Context) {
	t := time.NewTicker(m.cfg.Sync)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.syncAll(ctx)
		m.sweep(ctx)
	}
}

// User is a person as a node knows them: their DID, the name they gave, and
// the node they registered on, where their copies of channels are.
type User struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Node    string    `json:"node"`
	Created time.Time `json:"created"`
}

// Membership puts a person in a channel, with the node their copy is on.
// Its ID is <channel>:<user>.
type Membership struct {
	Channel string    `json:"channel"`
	User    string    `json:"user"`
	Node    string    `json:"node"`
	Since   time.Time `json:"since"`
}

// Channel is a channel or a direct message. Its ID is the hash of the event
// that created it, and Heads are the latest events, which the next names as
// parents. Members is filled in answers, not stored: memberships are.
type Channel struct {
	ID        string       `json:"id"`
	Name      string       `json:"name,omitempty"`
	Kind      string       `json:"kind"`
	Created   time.Time    `json:"created"`
	CreatedBy string       `json:"created_by"`
	Heads     []string     `json:"heads"`
	Members   []Membership `json:"members,omitempty"`
}

// Event is an event as stored and answered: the graph's event, its ID, and
// what the author signed, for other nodes to verify.
type Event struct {
	ID string `json:"id"`
	graph.Event
	Signed did.Signed `json:"signed"`
}

// member is one of a channel's initial members, in a create event's body.
type member struct {
	User string `json:"user"`
	Node string `json:"node"`
}

// notFound is a store's "no such record".
func notFound(err error) bool { return module.Code(err) == module.CodeNotFound }

func (m *Module) register(ctx context.Context, in struct {
	Signed did.Signed `json:"signed"`
}) (User, error) {
	who, err := in.Signed.Verify(UserPurpose, time.Now())
	if err != nil {
		return User{}, module.Errorf(module.CodeInvalidArgument, "%v", err)
	}
	var claim struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(in.Signed.Data, &claim); err != nil || claim.Name == "" || len(claim.Name) > 80 {
		return User{}, module.Errorf(module.CodeInvalidArgument, "a registration is {name, time}, the name 1 to 80 characters")
	}
	var u User
	version, err := m.env.Entities("user").Get(ctx, who, &u)
	if notFound(err) {
		u = User{ID: who, Node: m.env.NodeID, Created: time.Now().UTC()}
	} else if err != nil {
		return User{}, err
	}
	if u.Node != m.env.NodeID {
		return User{}, module.Errorf(module.CodePermissionDenied, "%s is registered on node %.8s", who, u.Node)
	}
	u.Name = claim.Name
	_, err = m.env.Entities("user").PutIf(ctx, u.ID, u, version)
	return u, err
}

func (m *Module) users(ctx context.Context, in struct {
	IDs []string `json:"ids"`
}) (map[string][]User, error) {
	users := []User{}
	if in.IDs == nil {
		records, err := m.env.Entities("user").Query(ctx, module.Query{OrderBy: "name", Limit: 1000})
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			var u User
			if err := r.Decode(&u); err != nil {
				return nil, err
			}
			users = append(users, u)
		}
	}
	for _, id := range in.IDs {
		var u User
		if _, err := m.env.Entities("user").Get(ctx, id, &u); notFound(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return map[string][]User{"users": users}, nil
}

func (m *Module) userInfo(ctx context.Context, in struct {
	ID string `json:"id"`
}) (User, error) {
	var u User
	if _, err := m.env.Entities("user").Get(ctx, in.ID, &u); notFound(err) || (err == nil && u.Node != m.env.NodeID) {
		return User{}, module.Errorf(module.CodeNotFound, "%s isn't registered here", in.ID)
	} else if err != nil {
		return User{}, err
	}
	return u, nil
}

// learn makes sure the node knows the person id, whose copies are on node:
// asking that node their name if it doesn't, and again later if it couldn't.
// Nodes trust what a person's own node says about their name, for now.
func (m *Module) learn(ctx context.Context, id, node string) {
	var u User
	version, err := m.env.Entities("user").Get(ctx, id, &u)
	if err == nil && (u.Node == m.env.NodeID || u.Name != placeholder(id)) {
		return
	}
	if err != nil {
		u = User{ID: id, Name: placeholder(id), Node: node, Created: time.Now().UTC()}
	}
	if node != m.env.NodeID {
		var theirs User
		ctx, cancel := context.WithTimeout(ctx, pushTimeout)
		defer cancel()
		if err := m.env.CallNode(ctx, node, Name+".user_info", map[string]string{"id": id}, &theirs); err == nil && theirs.ID == id {
			u.Name = theirs.Name
		}
	}
	if _, err := m.env.Entities("user").PutIf(ctx, id, u, version); err != nil && module.Code(err) != module.CodeConflict {
		slog.Warn("chat: can't keep a person", "id", id, "err", err)
	}
}

// placeholder names a person whose node couldn't be asked yet.
func placeholder(id string) string { return id[len(id)-8:] }

// memberships returns who is in the channel id.
func (m *Module) memberships(ctx context.Context, id string) ([]Membership, error) {
	records, err := m.env.Entities("membership").Query(ctx, module.Query{Where: map[string]any{"channel": id}, Limit: 1000})
	if err != nil {
		return nil, err
	}
	var ms []Membership
	for _, r := range records {
		var ms1 Membership
		if err := r.Decode(&ms1); err != nil {
			return nil, err
		}
		ms = append(ms, ms1)
	}
	slices.SortFunc(ms, func(a, b Membership) int { return cmpStrings(a.User, b.User) })
	return ms, nil
}

func cmpStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// channel returns the channel id, with who is in it.
func (m *Module) channel(ctx context.Context, id string) (Channel, int64, error) {
	var ch Channel
	version, err := m.env.Entities("channel").Get(ctx, id, &ch)
	if notFound(err) {
		return ch, 0, module.Errorf(module.CodeNotFound, "no channel %.8s", id)
	} else if err != nil {
		return ch, 0, err
	}
	if ch.Members, err = m.memberships(ctx, id); err != nil {
		return ch, 0, err
	}
	return ch, version, nil
}

// allChannels returns every channel this node has a copy of.
func (m *Module) allChannels(ctx context.Context) ([]Channel, error) {
	records, err := m.env.Entities("channel").Query(ctx, module.Query{OrderBy: "created", Limit: 1000})
	if err != nil {
		return nil, err
	}
	var channels []Channel
	for _, r := range records {
		ch, _, err := m.channel(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, nil
}

// in reports whether user is in ch.
func in(ch Channel, user string) bool {
	return slices.ContainsFunc(ch.Members, func(ms Membership) bool { return ms.User == user })
}

// people returns the members' DIDs.
func people(members []Membership) []string {
	var out []string
	for _, ms := range members {
		if !slices.Contains(out, ms.User) {
			out = append(out, ms.User)
		}
	}
	return out
}

// nodes returns the nodes with a copy of ch: its members' nodes.
func nodes(ch Channel) []string {
	var out []string
	for _, ms := range ch.Members {
		if !slices.Contains(out, ms.Node) {
			out = append(out, ms.Node)
		}
	}
	return out
}

// others returns the nodes with a copy of ch besides this one.
func (m *Module) others(ch Channel) []string {
	return slices.DeleteFunc(nodes(ch), func(n string) bool { return n == m.env.NodeID })
}

// submit takes an event a person here signed.
func (m *Module) submit(ctx context.Context, in struct {
	Signed did.Signed `json:"signed"`
}) (Event, error) {
	e, fresh, err := m.apply(ctx, in.Signed, "")
	if err != nil {
		return Event{}, err
	}
	if fresh {
		ch, _, err := m.channel(ctx, e.Channel)
		if err != nil {
			return Event{}, err
		}
		for _, node := range m.others(ch) {
			go m.pushTo(node, e.Channel, []did.Signed{in.Signed})
		}
	}
	return e, nil
}

// missing is what a node lacks to apply an event: its parents.
type missing []string

func (ids missing) Error() string { return fmt.Sprintf("missing %d parents", len(ids)) }

// apply checks a signed event and stores it, telling subscribers, unless
// it's there already: fresh says which. from is the node that pushed it,
// or "" for a person here. It fails with missing if parents aren't here.
func (m *Module) apply(ctx context.Context, s did.Signed, from string) (Event, bool, error) {
	id, ev, err := graph.Open(s, time.Now())
	if err != nil {
		return Event{}, false, module.Errorf(module.CodeInvalidArgument, "%v", err)
	}
	e := Event{ID: id, Event: ev, Signed: s}
	var have Event
	if _, err := m.env.Entities("event").Get(ctx, id, &have); err == nil {
		return have, false, nil
	}
	if ev.Kind == "create" {
		e.Channel = id // the create event is the channel
		if err := m.create(ctx, e, from); err != nil {
			return Event{}, false, err
		}
		return e, true, m.posted(ctx, e)
	}
	ch, version, err := m.channel(ctx, ev.Channel)
	if err != nil {
		return Event{}, false, err
	}
	if !in(ch, ev.Author) {
		return Event{}, false, module.Errorf(module.CodePermissionDenied, "%s isn't in channel %.8s", ev.Author, ch.ID)
	}
	if from == "" && !slices.ContainsFunc(ch.Members, func(ms Membership) bool { return ms.User == ev.Author && ms.Node == m.env.NodeID }) {
		return Event{}, false, module.Errorf(module.CodePermissionDenied, "%s isn't registered here", ev.Author)
	}
	var lacking missing
	for _, p := range ev.Parents {
		var parent Event
		if _, err := m.env.Entities("event").Get(ctx, p, &parent); notFound(err) {
			lacking = append(lacking, p)
		} else if err != nil {
			return Event{}, false, err
		} else if parent.Channel != ch.ID {
			return Event{}, false, module.Errorf(module.CodeInvalidArgument, "parent %.8s is in another channel", p)
		}
	}
	if len(lacking) > 0 {
		return Event{}, false, lacking
	}
	if len(ev.Parents) == 0 {
		return Event{}, false, module.Errorf(module.CodeInvalidArgument, "an event names the latest events it saw as parents")
	}
	extra, err := m.check(ctx, ch, e)
	if err != nil {
		return Event{}, false, err
	}
	// The event becomes a head, in place of the parents it names.
	for range retries {
		heads := slices.DeleteFunc(slices.Clone(ch.Heads), func(h string) bool { return slices.Contains(ev.Parents, h) })
		heads = append(heads, id)
		slices.Sort(heads)
		b := m.env.Batch().PutIf("event", id, e, 0)
		b.PutIf("channel", ch.ID, Channel{ID: ch.ID, Name: ch.Name, Kind: ch.Kind, Created: ch.Created, CreatedBy: ch.CreatedBy, Heads: heads}, version)
		if extra != nil {
			extra(b)
		}
		_, err = b.Commit(ctx)
		if module.Code(err) != module.CodeConflict {
			break
		}
		if ch, version, err = m.channel(ctx, ch.ID); err != nil {
			return Event{}, false, err
		}
	}
	if err != nil {
		return Event{}, false, err
	}
	return e, true, m.posted(ctx, e)
}

// posted tells the apps of the channel's members here of an event added to
// it: the members as they are now, so an invite reaches the invited.
func (m *Module) posted(ctx context.Context, e Event) error {
	members, err := m.memberships(ctx, e.Channel)
	if err != nil {
		return err
	}
	return m.env.EmitTo(ctx, "posted", e, people(members))
}

// create stores a channel from its create event: the channel, its first
// members, and the event. from is the node that pushed it, or "".
func (m *Module) create(ctx context.Context, e Event, from string) error {
	var body struct {
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Members []member `json:"members"`
	}
	if err := json.Unmarshal(e.Body, &body); err != nil {
		return module.Errorf(module.CodeInvalidArgument, "create: %v", err)
	}
	if body.Kind == "" {
		body.Kind = "channel"
	}
	if body.Kind != "channel" && body.Kind != "dm" {
		return module.Errorf(module.CodeInvalidArgument, "create: kind must be channel or dm")
	}
	if !slices.ContainsFunc(body.Members, func(mb member) bool { return mb.User == e.Author }) {
		return module.Errorf(module.CodeInvalidArgument, "create: the author must be a member")
	}
	if from == "" && !slices.ContainsFunc(body.Members, func(mb member) bool { return mb.User == e.Author && mb.Node == m.env.NodeID }) {
		return module.Errorf(module.CodePermissionDenied, "%s isn't registered here", e.Author)
	}
	for _, mb := range body.Members {
		if mb.User == "" || mb.Node == "" {
			return module.Errorf(module.CodeInvalidArgument, "create: every member is {user, node}")
		}
		m.learn(ctx, mb.User, mb.Node)
	}
	ch := Channel{ID: e.ID, Name: body.Name, Kind: body.Kind, Created: e.Time, CreatedBy: e.Author, Heads: []string{e.ID}}
	b := m.env.Batch().PutIf("event", e.ID, e, 0).PutIf("channel", ch.ID, ch, 0)
	seen := map[string]bool{}
	for _, mb := range body.Members {
		if !seen[mb.User] {
			seen[mb.User] = true
			b.PutIf("membership", ch.ID+":"+mb.User, Membership{Channel: ch.ID, User: mb.User, Node: mb.Node, Since: e.Time}, 0)
		}
	}
	_, err := b.Commit(ctx)
	return err
}

// check checks an event against its channel, and returns what else to write
// with it, if anything.
func (m *Module) check(ctx context.Context, ch Channel, e Event) (func(*module.Batch), error) {
	var body struct {
		Text  string `json:"text"`
		Event string `json:"event"`
		Emoji string `json:"emoji"`
		User  string `json:"user"`
		Node  string `json:"node"`
	}
	if len(e.Body) > 0 {
		if err := json.Unmarshal(e.Body, &body); err != nil {
			return nil, module.Errorf(module.CodeInvalidArgument, "%s: %v", e.Kind, err)
		}
	}
	target := func(message bool) (Event, error) {
		var t Event
		if _, err := m.env.Entities("event").Get(ctx, body.Event, &t); err != nil || t.Channel != ch.ID {
			return t, module.Errorf(module.CodeNotFound, "no event %.8s in channel %.8s", body.Event, ch.ID)
		}
		if message && t.Kind != "message" {
			return t, module.Errorf(module.CodeInvalidArgument, "event %.8s isn't a message", body.Event)
		}
		return t, nil
	}
	switch e.Kind {
	case "message":
		if body.Text == "" || len(body.Text) > 4000 {
			return nil, module.Errorf(module.CodeInvalidArgument, "a message is 1 to 4000 characters")
		}
	case "edit":
		t, err := target(true)
		if err != nil {
			return nil, err
		}
		if t.Author != e.Author {
			return nil, module.Errorf(module.CodePermissionDenied, "only the author may edit a message")
		}
		if body.Text == "" || len(body.Text) > 4000 {
			return nil, module.Errorf(module.CodeInvalidArgument, "a message is 1 to 4000 characters")
		}
	case "reaction":
		if _, err := target(true); err != nil {
			return nil, err
		}
		if body.Emoji == "" || len(body.Emoji) > 16 {
			return nil, module.Errorf(module.CodeInvalidArgument, "a reaction is an emoji")
		}
	case "receipt":
		if _, err := target(false); err != nil {
			return nil, err
		}
	case "invite":
		if body.User == "" || body.Node == "" {
			return nil, module.Errorf(module.CodeInvalidArgument, "an invite is {user, node}")
		}
		if in(ch, body.User) {
			return nil, module.Errorf(module.CodeInvalidArgument, "%s is in the channel already", body.User)
		}
		m.learn(ctx, body.User, body.Node)
		return func(b *module.Batch) {
			b.PutIf("membership", ch.ID+":"+body.User, Membership{Channel: ch.ID, User: body.User, Node: body.Node, Since: e.Time}, 0)
		}, nil
	default:
		return nil, module.Errorf(module.CodeInvalidArgument, "unknown event kind %q", e.Kind)
	}
	return nil, nil
}

// pushTo pushes events of a channel to another member's node, trying again
// a few times if it can't be reached; the sync then catches it up.
func (m *Module) pushTo(node, channel string, events []did.Signed) {
	delay := 5 * time.Second
	for attempt := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
		err := m.env.CallNode(ctx, node, Name+".push", map[string]any{"channel": channel, "events": events}, nil)
		cancel()
		if err == nil {
			return
		}
		if attempt == 2 {
			slog.Warn("chat: push failed; the sync will catch up", "node", node, "channel", channel, "err", err)
			return
		}
		time.Sleep(delay)
		delay *= 6
	}
}

// push takes a channel's new events from another member's node, fetching the
// channel, or missing parents, from it first.
func (m *Module) push(ctx context.Context, in struct {
	Channel string       `json:"channel"`
	Events  []did.Signed `json:"events"`
}) (struct{}, error) {
	from, err := m.fromPeer(ctx, in.Channel)
	if err != nil {
		return struct{}{}, err
	}
	for _, s := range in.Events {
		_, _, err := m.apply(ctx, s, from)
		var lacking missing
		if errors.As(err, &lacking) {
			if err = m.fetchFrom(ctx, from, in.Channel, lacking); err == nil {
				_, _, err = m.apply(ctx, s, from)
			}
		}
		if err != nil {
			return struct{}{}, err
		}
	}
	return struct{}{}, nil
}

// fromPeer checks that a call about channel comes from another member's
// node, copying the channel from it first if this node lacks it, and
// returns that node.
func (m *Module) fromPeer(ctx context.Context, channel string) (string, error) {
	from := module.Caller(ctx)
	if from == m.env.NodeID {
		return "", module.Errorf(module.CodePermissionDenied, "for other nodes; people submit")
	}
	ch, _, err := m.channel(ctx, channel)
	if notFound(err) {
		if err := m.fetchFrom(ctx, from, channel, nil); err != nil {
			return "", err
		}
		if ch, _, err = m.channel(ctx, channel); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if !slices.Contains(nodes(ch), from) {
		return "", module.Errorf(module.CodePermissionDenied, "node %.8s has no one in channel %.8s", from, ch.ID)
	}
	return from, nil
}

// fetched is what fetch answers.
type fetched struct {
	Channel     Channel      `json:"channel"`
	Memberships []Membership `json:"memberships"`
	Events      []did.Signed `json:"events"`
}

// fetchFrom copies a channel from the node from: all of it, or the events
// ids and whatever they need, however far back. Someone here must be in it.
func (m *Module) fetchFrom(ctx context.Context, from, channel string, ids []string) error {
	events := map[string]did.Signed{}
	var pending []string
	fetch := func(ids []string) error {
		var got fetched
		in := map[string]any{"channel": channel}
		if ids != nil {
			in["ids"] = ids
		}
		if err := m.env.CallNode(ctx, from, Name+".fetch", in, &got); err != nil {
			return err
		}
		if got.Channel.ID != channel {
			return module.Errorf(module.CodeInvalidArgument, "node %.8s answered with another channel", from)
		}
		if ids == nil && !slices.ContainsFunc(got.Memberships, func(ms Membership) bool { return ms.Node == m.env.NodeID }) {
			return module.Errorf(module.CodePermissionDenied, "no one here is in channel %.8s", channel)
		}
		for _, s := range got.Events {
			if id := graph.ID(s.Data); events[id].Data == nil {
				events[id] = s
				pending = append(pending, id)
			}
		}
		return nil
	}
	if err := fetch(ids); err != nil {
		return err
	}
	// Events verify themselves; applying them rebuilds the channel and its
	// memberships, so nothing is taken on the other node's word. Whatever
	// they need that wasn't sent is fetched too.
	for round := 0; len(pending) > 0; round++ {
		var later []string
		need := map[string]bool{}
		for _, id := range pending {
			_, _, err := m.apply(ctx, events[id], from)
			var lacking missing
			if errors.As(err, &lacking) {
				later = append(later, id)
				for _, p := range lacking {
					if events[p].Data == nil {
						need[p] = true
					}
				}
				continue
			}
			if err != nil {
				return err
			}
		}
		stuck := len(later) == len(pending)
		pending = later
		if len(need) > 0 {
			if round >= fetchRounds {
				return module.Errorf(module.CodeUnavailable, "channel %.8s goes back too far to fetch", channel)
			}
			if err := fetch(slices.Sorted(maps.Keys(need))); err != nil {
				return err
			}
		} else if stuck {
			return module.Errorf(module.CodeInvalidArgument, "node %.8s left out %d events' parents", from, len(later))
		}
	}
	return nil
}

// fetch answers another member's node with a channel: all of it, or the
// events asked for.
func (m *Module) fetch(ctx context.Context, in struct {
	Channel string   `json:"channel"`
	IDs     []string `json:"ids"`
}) (fetched, error) {
	from := module.Caller(ctx)
	ch, _, err := m.channel(ctx, in.Channel)
	if err != nil {
		return fetched{}, err
	}
	if from == m.env.NodeID || !slices.Contains(nodes(ch), from) {
		return fetched{}, module.Errorf(module.CodePermissionDenied, "node %.8s has no one in channel %.8s", from, ch.ID)
	}
	out := fetched{Channel: ch, Memberships: ch.Members}
	out.Channel.Members = nil
	if in.IDs != nil {
		for _, id := range in.IDs {
			var e Event
			if _, err := m.env.Entities("event").Get(ctx, id, &e); err == nil && e.Channel == ch.ID {
				out.Events = append(out.Events, e.Signed)
			}
		}
		return out, nil
	}
	events, err := m.ordered(ctx, ch.ID)
	if err != nil {
		return fetched{}, err
	}
	for _, e := range events {
		out.Events = append(out.Events, e.Signed)
	}
	return out, nil
}

// sync takes another member's node's heads for a channel, fetches what this
// node lacks of them, and answers with its own heads, for the other node to
// do the same: afterwards both have everything.
func (m *Module) sync(ctx context.Context, in struct {
	Channel string   `json:"channel"`
	Heads   []string `json:"heads"`
}) (map[string][]string, error) {
	from, err := m.fromPeer(ctx, in.Channel)
	if err != nil {
		return nil, err
	}
	if err := m.catchUp(ctx, from, in.Channel, in.Heads); err != nil {
		return nil, err
	}
	ch, _, err := m.channel(ctx, in.Channel)
	if err != nil {
		return nil, err
	}
	return map[string][]string{"heads": ch.Heads}, nil
}

// catchUp fetches from the node from the heads of channel it lacks, and so
// everything behind them.
func (m *Module) catchUp(ctx context.Context, from, channel string, heads []string) error {
	var lacking []string
	for _, h := range heads {
		var e Event
		if _, err := m.env.Entities("event").Get(ctx, h, &e); notFound(err) {
			lacking = append(lacking, h)
		} else if err != nil {
			return err
		}
	}
	if len(lacking) == 0 {
		return nil
	}
	return m.fetchFrom(ctx, from, channel, lacking)
}

// syncAll catches up with the other members' nodes of every channel.
func (m *Module) syncAll(ctx context.Context) {
	channels, err := m.allChannels(ctx)
	if err != nil {
		slog.Warn("chat: can't list channels to sync", "err", err)
		return
	}
	for _, ch := range channels {
		for _, node := range m.others(ch) {
			if err := m.syncWith(ctx, node, ch); err != nil {
				slog.Debug("chat: sync failed", "node", node, "channel", ch.ID, "err", err)
				continue
			}
			// The node answers, so people there whose names couldn't be
			// asked before can be now.
			for _, ms := range ch.Members {
				if ms.Node == node {
					m.learn(ctx, ms.User, node)
				}
			}
		}
	}
}

// syncWith exchanges heads of ch with the node node, and fetches what this
// node lacks.
func (m *Module) syncWith(ctx context.Context, node string, ch Channel) error {
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	var theirs struct {
		Heads []string `json:"heads"`
	}
	if err := m.env.CallNode(ctx, node, Name+".sync", map[string]any{"channel": ch.ID, "heads": ch.Heads}, &theirs); err != nil {
		return err
	}
	return m.catchUp(ctx, node, ch.ID, theirs.Heads)
}

// signedIn returns the person a call is for, who must have signed in to the
// node.
func signedIn(ctx context.Context) (string, error) {
	user := module.User(ctx)
	if user == "" {
		return "", module.Errorf(module.CodePermissionDenied, "sign in first")
	}
	return user, nil
}

func (m *Module) channels(ctx context.Context, _ struct{}) (map[string][]Channel, error) {
	user, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	channels, err := m.channelsOf(ctx, user)
	if err != nil {
		return nil, err
	}
	return map[string][]Channel{"channels": channels}, nil
}

// channelsOf returns the channels the person is in, oldest first.
func (m *Module) channelsOf(ctx context.Context, user string) ([]Channel, error) {
	records, err := m.env.Entities("membership").Query(ctx, module.Query{Where: map[string]any{"user": user}, Limit: 1000})
	if err != nil {
		return nil, err
	}
	channels := []Channel{}
	for _, r := range records {
		var ms Membership
		if err := r.Decode(&ms); err != nil {
			return nil, err
		}
		ch, _, err := m.channel(ctx, ms.Channel)
		if err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	slices.SortFunc(channels, func(a, b Channel) int { return a.Created.Compare(b.Created) })
	return channels, nil
}

// ordered returns a channel's events in causal order: every node orders a
// channel the same way, by the graph.
func (m *Module) ordered(ctx context.Context, channel string) ([]Event, error) {
	records, err := m.env.Entities("event").Query(ctx, module.Query{Where: map[string]any{"channel": channel}, OrderBy: "time", Limit: 1000})
	if err != nil {
		return nil, err
	}
	g := graph.New(channel)
	byID := map[string]Event{}
	for _, r := range records {
		var e Event
		if err := r.Decode(&e); err != nil {
			return nil, err
		}
		if _, err := g.Add(e.ID, e.Event); err != nil {
			return nil, fmt.Errorf("corrupt channel %.8s: %w", channel, err)
		}
		byID[e.ID] = e
	}
	var events []Event
	for _, id := range g.Order() {
		events = append(events, byID[id])
	}
	return events, nil
}

func (m *Module) history(ctx context.Context, req struct {
	Channel string `json:"channel"`
	Limit   int    `json:"limit"`
}) (map[string][]Event, error) {
	if req.Limit == 0 {
		req.Limit = defaultHistory
	}
	user, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	ch, _, err := m.channel(ctx, req.Channel)
	if err != nil {
		return nil, err
	}
	if !in(ch, user) {
		return nil, module.Errorf(module.CodePermissionDenied, "%s isn't in channel %.8s", user, ch.ID)
	}
	events, err := m.ordered(ctx, req.Channel)
	if err != nil {
		return nil, err
	}
	if len(events) > req.Limit {
		events = events[len(events)-req.Limit:]
	}
	if events == nil {
		events = []Event{}
	}
	return map[string][]Event{"events": events}, nil
}

// notice is what member nodes tell each other that isn't an event: someone
// typing in a channel, or someone going online or offline.
type notice struct {
	Kind    string `json:"kind"` // typing or presence
	User    string `json:"user"`
	Channel string `json:"channel,omitempty"` // typing
	Online  bool   `json:"online,omitempty"`  // presence
}

// tell sends a notice to other nodes, without waiting.
func (m *Module) tell(ctx context.Context, nodes []string, n notice) {
	for _, node := range nodes {
		if node != m.env.NodeID {
			_ = m.env.NotifyNode(ctx, node, Name+".notice", n)
		}
	}
}

func (m *Module) typing(ctx context.Context, req struct {
	Channel string `json:"channel"`
}) (struct{}, error) {
	user, err := signedIn(ctx)
	if err != nil {
		return struct{}{}, err
	}
	ch, _, err := m.channel(ctx, req.Channel)
	if err != nil {
		return struct{}{}, err
	}
	if !in(ch, user) {
		return struct{}{}, module.Errorf(module.CodePermissionDenied, "%s isn't in channel %.8s", user, req.Channel)
	}
	m.tell(ctx, m.others(ch), notice{Kind: "typing", User: user, Channel: req.Channel})
	return struct{}{}, m.env.EmitTo(ctx, "typing", map[string]string{"user": user, "channel": req.Channel}, people(ch.Members))
}

// heartbeat is from the client of the person signed in: they're online until
// the heartbeats stop.
func (m *Module) heartbeat(ctx context.Context, _ struct{}) (struct{}, error) {
	user, err := signedIn(ctx)
	if err != nil {
		return struct{}{}, err
	}
	var u User
	if _, err := m.env.Entities("user").Get(ctx, user, &u); err != nil || u.Node != m.env.NodeID {
		return struct{}{}, module.Errorf(module.CodeNotFound, "%s isn't registered here", user)
	}
	m.mu.Lock()
	_, was := m.online[user]
	m.online[user] = time.Now()
	m.mu.Unlock()
	if was {
		return struct{}{}, nil
	}
	return struct{}{}, m.announce(ctx, user, true)
}

// announce tells the apps of the people who share a channel with a person
// here, and the other members' nodes, that they went online or offline.
func (m *Module) announce(ctx context.Context, user string, online bool) error {
	n := notice{Kind: "presence", User: user, Online: online}
	nodes, people, err := m.sharing(ctx, user)
	if err != nil {
		return err
	}
	m.tell(ctx, nodes, n)
	return m.env.EmitTo(ctx, "presence", map[string]any{"user": user, "online": online}, people)
}

// sharing returns the nodes and the people that share a channel with the
// person id, the person among them.
func (m *Module) sharing(ctx context.Context, id string) (withNodes, withPeople []string, err error) {
	channels, err := m.channelsOf(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	for _, ch := range channels {
		for _, node := range nodes(ch) {
			if !slices.Contains(withNodes, node) {
				withNodes = append(withNodes, node)
			}
		}
		for _, p := range people(ch.Members) {
			if !slices.Contains(withPeople, p) {
				withPeople = append(withPeople, p)
			}
		}
	}
	return withNodes, withPeople, nil
}

// sweep lets people go offline whose heartbeats stopped.
func (m *Module) sweep(ctx context.Context) {
	m.mu.Lock()
	var gone []string
	for user, seen := range m.online {
		if time.Since(seen) > onlineFor {
			gone = append(gone, user)
			delete(m.online, user)
		}
	}
	m.mu.Unlock()
	for _, user := range gone {
		var u User
		if _, err := m.env.Entities("user").Get(ctx, user, &u); err == nil && u.Node == m.env.NodeID {
			_ = m.announce(ctx, user, false)
		} else if _, people, err := m.sharing(ctx, user); err == nil {
			_ = m.env.EmitTo(ctx, "presence", map[string]any{"user": user, "online": false}, people)
		}
	}
}

func (m *Module) whoIsOnline(ctx context.Context, in struct {
	Users []string `json:"users"`
}) (map[string][]string, error) {
	if _, err := signedIn(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	online := []string{}
	for _, u := range in.Users {
		if seen, ok := m.online[u]; ok && time.Since(seen) <= onlineFor {
			online = append(online, u)
		}
	}
	return map[string][]string{"online": online}, nil
}

// notice takes typing and presence from another member's node.
func (m *Module) notice(ctx context.Context, n notice) (struct{}, error) {
	from := module.Caller(ctx)
	if from == m.env.NodeID {
		return struct{}{}, module.Errorf(module.CodePermissionDenied, "notices are from other nodes")
	}
	switch n.Kind {
	case "typing":
		ch, _, err := m.channel(ctx, n.Channel)
		if err != nil {
			return struct{}{}, err
		}
		if !slices.Contains(nodes(ch), from) || !in(ch, n.User) {
			return struct{}{}, module.Errorf(module.CodePermissionDenied, "not a member")
		}
		return struct{}{}, m.env.EmitTo(ctx, "typing", map[string]string{"user": n.User, "channel": n.Channel}, people(ch.Members))
	case "presence":
		shared, people, err := m.sharing(ctx, n.User)
		if err != nil {
			return struct{}{}, err
		}
		if !slices.Contains(shared, from) {
			return struct{}{}, module.Errorf(module.CodePermissionDenied, "not a member")
		}
		m.mu.Lock()
		if n.Online {
			m.online[n.User] = time.Now()
		} else {
			delete(m.online, n.User)
		}
		m.mu.Unlock()
		return struct{}{}, m.env.EmitTo(ctx, "presence", map[string]any{"user": n.User, "online": n.Online}, people)
	}
	return struct{}{}, module.Errorf(module.CodeInvalidArgument, "unknown notice %q", n.Kind)
}
