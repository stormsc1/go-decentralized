package graph

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"go-decentralized/did"
)

// event makes an event in channel, at ms milliseconds, and its ID. Events
// without parents create their channel.
func event(channel string, ms int64, text string, parents ...string) (string, Event) {
	e := Event{Channel: channel, Author: "did:key:z6MkTest", Kind: "message", Parents: parents, Time: time.UnixMilli(ms).UTC()}
	if len(parents) == 0 {
		e.Kind = "create"
	}
	e.Body, _ = json.Marshal(map[string]string{"text": text})
	data, _ := json.Marshal(e)
	return ID(data), e
}

// channel is a channel's events: b and c happen at once, a later but
// concurrently, and m after all three, by a clock that's behind.
type channel struct {
	ids    []string
	events map[string]Event
	order  []string
}

func newChannel() channel {
	root, create := event("", 0, "")
	a, ea := event(root, 10, "a", root)
	b, eb := event(root, 5, "b", root)
	c, ec := event(root, 5, "c", root)
	m, em := event(root, 1, "m", a, b, c)
	ch := channel{
		ids:    []string{root, a, b, c, m},
		events: map[string]Event{root: create, a: ea, b: eb, c: ec, m: em},
	}
	tied := []string{b, c}
	slices.Sort(tied)
	ch.order = slices.Concat([]string{root}, tied, []string{a, m})
	return ch
}

// Every node sorts the same events the same way, in whatever order they
// arrive.
func TestOrder(t *testing.T) {
	ch := newChannel()
	root, m := ch.ids[0], ch.ids[4]
	for _, arrival := range permutations(ch.ids) {
		g := New(root)
		var added []string
		for _, id := range arrival {
			ids, err := g.Add(id, ch.events[id])
			if err != nil {
				t.Fatal(err)
			}
			added = append(added, ids...)
		}
		if got := g.Order(); !slices.Equal(got, ch.order) {
			t.Fatalf("arriving as %.4s, the order is %.4s, want %.4s", arrival, got, ch.order)
		}
		if len(added) != len(ch.ids) {
			t.Fatalf("arriving as %.4s, Add added %.4s", arrival, added)
		}
		if heads, missing := g.Heads(), g.Missing(); !slices.Equal(heads, []string{m}) || len(missing) > 0 {
			t.Fatalf("heads %.4s, missing %.4s", heads, missing)
		}
	}
}

// Events wait for their parents, and the graph says which to fetch.
func TestWaiting(t *testing.T) {
	ch := newChannel()
	root, a, b, c, m := ch.ids[0], ch.ids[1], ch.ids[2], ch.ids[3], ch.ids[4]
	g := New(root)
	add := func(id string, want ...string) {
		t.Helper()
		added, err := g.Add(id, ch.events[id])
		if err != nil || !slices.Equal(added, want) {
			t.Fatalf("Add(%.4s) = %.4s, %v; want %.4s", id, added, err, want)
		}
	}
	missing := func(want ...string) {
		t.Helper()
		slices.Sort(want)
		if got := g.Missing(); !slices.Equal(got, want) {
			t.Fatalf("missing %.4s, want %.4s", got, want)
		}
	}
	add(m)
	missing(a, b, c)
	add(a)
	missing(b, c, root)
	if _, ok := g.Get(a); ok || !g.Has(a) {
		t.Fatal("a waiting event is in the order, or not in the graph")
	}
	add(root, root, a)
	add(a) // again: nothing new
	add(b, b)
	add(c, c, m)
	missing()
	if got := g.Order(); !slices.Equal(got, ch.order) {
		t.Fatalf("order %.4s, want %.4s", got, ch.order)
	}
}

func TestRejects(t *testing.T) {
	ch := newChannel()
	root := ch.ids[0]
	g := New(root)
	other, create := event("", 1, "")
	elsewhere, e := event(other, 1, "", root)
	twice, et := event(root, 1, "", root, root)
	for id, e := range map[string]Event{
		other:     create, // creates another channel
		elsewhere: e,
		twice:     et,
		"self":    {Channel: root, Parents: []string{"self"}},
	} {
		if _, err := g.Add(id, e); err == nil {
			t.Errorf("added %.8s: %+v", id, e)
		}
	}
}

// Events are signed by their authors, and their IDs are hashes of what they
// signed.
func TestOpen(t *testing.T) {
	author, key, _ := did.New()
	someone, _, _ := did.New()
	now := time.Now()
	for _, c := range []struct {
		author, purpose string
		ok              bool
	}{
		{author, Purpose, true},
		{someone, Purpose, false},     // signed by another
		{author, "chat.other", false}, // for something else
	} {
		data, _ := json.Marshal(Event{Author: c.author, Kind: "create", Time: now})
		s, err := did.Sign(key, c.purpose, data)
		if err != nil {
			t.Fatal(err)
		}
		id, e, err := Open(s, now)
		if ok := err == nil && id == ID(data) && e.Author == author; ok != c.ok {
			t.Errorf("Open by %.20s for %s = %.8s, %+v, %v", c.author, c.purpose, id, e, err)
		}
	}
}

// permutations returns every order of ids.
func permutations(ids []string) [][]string {
	if len(ids) <= 1 {
		return [][]string{slices.Clone(ids)}
	}
	var out [][]string
	for i, id := range ids {
		rest := slices.Concat(ids[:i], ids[i+1:])
		for _, p := range permutations(rest) {
			out = append(out, append([]string{id}, p...))
		}
	}
	return out
}
