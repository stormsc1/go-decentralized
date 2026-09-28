package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"testing"
	"time"

	"go-decentralized/internal/node"
	"go-decentralized/module"
)

// TestLab drives the chat across two nodes of the Docker lab (compose.yaml)
// over their local APIs: gl on the public internet and kg behind the
// corporate NAT, reached through the relay. It runs when CHAT_LAB is set,
// with GL_API and KG_API overriding the APIs compose publishes.
func TestLab(t *testing.T) {
	if os.Getenv("CHAT_LAB") == "" {
		t.Skip("set CHAT_LAB=1 with the lab up (docker compose up -d)")
	}
	gl := newAPI(t, envOr("GL_API", "http://localhost:8443"))
	kg := newAPI(t, envOr("KG_API", "http://localhost:8444"))
	glID, kgID := gl.nodeID(), kg.nodeID()
	alice, bob := newPerson(t), newPerson(t)

	// Alice lives on gl, Bob on kg; each signs in to their node.
	var au, bu User
	gl.must("register", map[string]any{"signed": alice.sign(t, UserPurpose, map[string]any{"name": "Alice (gl)", "time": time.Now()})}, &au)
	kg.must("register", map[string]any{"signed": bob.sign(t, UserPurpose, map[string]any{"name": "Bob (kg)", "time": time.Now()})}, &bu)
	if au.Node != glID || bu.Node != kgID {
		t.Fatalf("alice on %.8s, bob on %.8s", au.Node, bu.Node)
	}
	gl.login(alice)
	kg.login(bob)

	// Alice makes a channel with Bob: gl asks kg for Bob's name and pushes
	// the channel to kg, through the relay.
	var created Event
	gl.must("submit", map[string]any{"signed": alice.create(t, "gl+kg", "", map[string]string{alice.id: glID, bob.id: kgID})}, &created)
	var known struct{ Users []User }
	gl.must("users", map[string]any{"ids": []string{bob.id}}, &known)
	if len(known.Users) != 1 || known.Users[0].Name != "Bob (kg)" {
		t.Fatalf("gl knows bob as %+v", known.Users)
	}
	var ch Channel
	eventually(t, "kg has the channel", func() bool {
		var out struct{ Channels []Channel }
		if kg.call("channels", nil, &out) != nil {
			return false
		}
		for _, c := range out.Channels {
			if c.ID == created.ID {
				ch = c
				return true
			}
		}
		return false
	})

	// Bob answers from kg; gl gets it. Alice replies; kg gets it.
	var m1, m2 Event
	kg.must("submit", map[string]any{"signed": bob.event(t, ch.ID, ch.Heads, "message", map[string]string{"text": "hello from behind the corporate NAT"})}, &m1)
	eventually(t, "gl has bob's message", func() bool { return gl.has(ch.ID, m1.ID) })
	gl.must("submit", map[string]any{"signed": alice.event(t, ch.ID, []string{m1.ID}, "message", map[string]string{"text": "hello from the public internet"})}, &m2)
	eventually(t, "kg has alice's message", func() bool { return kg.has(ch.ID, m2.ID) })
	if a, b := gl.kinds(ch.ID), kg.kinds(ch.ID); a != b || a != "create,message,message" {
		t.Fatalf("gl: %s, kg: %s", a, b)
	}
	t.Logf("channel %.8s shared by gl and kg: %s", ch.ID, gl.kinds(ch.ID))
}

// TestLabCatchUp stops kg, posts from gl while it's down, starts kg again
// and waits for it to catch up: through the push's retries, or the sync.
// It takes a few minutes, and needs docker compose.
func TestLabCatchUp(t *testing.T) {
	if os.Getenv("CHAT_LAB") == "" {
		t.Skip("set CHAT_LAB=1 with the lab up (docker compose up -d)")
	}
	compose := func(args ...string) {
		t.Helper()
		out, err := exec.Command("docker", append([]string{"compose"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker compose %v: %v\n%s", args, err, out)
		}
	}
	gl := newAPI(t, envOr("GL_API", "http://localhost:8443"))
	kg := newAPI(t, envOr("KG_API", "http://localhost:8444"))
	glID, kgID := gl.nodeID(), kg.nodeID()
	alice, bob := newPerson(t), newPerson(t)
	gl.must("register", map[string]any{"signed": alice.sign(t, UserPurpose, map[string]any{"name": "Alice (gl)", "time": time.Now()})}, nil)
	kg.must("register", map[string]any{"signed": bob.sign(t, UserPurpose, map[string]any{"name": "Bob (kg)", "time": time.Now()})}, nil)
	gl.login(alice)
	kg.login(bob)
	var created Event
	gl.must("submit", map[string]any{"signed": alice.create(t, "catch-up", "", map[string]string{alice.id: glID, bob.id: kgID})}, &created)
	eventually(t, "kg has the channel", func() bool { return kg.has(created.ID, created.ID) })

	compose("stop", "kg")
	defer compose("start", "kg")
	var m1 Event
	gl.must("submit", map[string]any{"signed": alice.event(t, created.ID, []string{created.ID}, "message", map[string]string{"text": "posted while kg was down"})}, &m1)
	compose("start", "kg")

	// kg restarted, so Bob signs in again once it answers.
	start := time.Now()
	for deadline := start.Add(4 * time.Minute); ; time.Sleep(2 * time.Second) {
		if kg.relogin(bob) && kg.has(created.ID, m1.ID) {
			t.Logf("kg caught up %s after coming back", time.Since(start).Round(time.Second))
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("kg never caught up")
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// api is a lab node's local API, with the session of whoever signed in.
type api struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newAPI(t *testing.T, base string) api {
	jar, _ := cookiejar.New(nil)
	return api{t, base, &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func (a api) nodeID() string {
	res, err := a.client.Get(a.base + "/v1/info")
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	var info struct{ ID string }
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil || info.ID == "" {
		a.t.Fatalf("%s/v1/info: %v", a.base, err)
	}
	return info.ID
}

// post posts JSON and returns the status and body.
func (a api) post(path string, in any) (int, []byte, error) {
	input, err := json.Marshal(in)
	if err != nil {
		a.t.Fatal(err)
	}
	res, err := a.client.Post(a.base+path, "application/json", bytes.NewReader(input))
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, body, nil
}

// login signs the person in: a challenge, signed for the node by their
// device.
func (a api) login(p person) {
	a.t.Helper()
	if !a.relogin(p) {
		a.t.Fatalf("%s: can't sign in", a.base)
	}
}

// relogin signs the person in, reporting whether it worked.
func (a api) relogin(p person) bool {
	res, err := a.client.Get(a.base + "/v1/challenge")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var challenge struct{ Challenge, Node string }
	if json.NewDecoder(res.Body).Decode(&challenge) != nil {
		return false
	}
	signed := p.sign(a.t, node.LoginPurpose, map[string]string{"challenge": challenge.Challenge, "node": challenge.Node})
	status, _, err := a.post("/v1/login", map[string]any{"signed": signed})
	return err == nil && status == http.StatusOK
}

func (a api) call(name string, in any, out any) error {
	status, body, err := a.post("/v1/capabilities/chat."+name, in)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		var e module.Error
		if json.Unmarshal(body, &e) == nil && e.Code != "" {
			return &e
		}
		return fmt.Errorf("%d: %s", status, body)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func (a api) must(name string, in any, out any) {
	a.t.Helper()
	if err := a.call(name, in, out); err != nil {
		a.t.Fatalf("%s %s: %v", a.base, name, err)
	}
}

func (a api) history(channel string) []Event {
	var out struct{ Events []Event }
	if err := a.call("history", map[string]any{"channel": channel}, &out); err != nil {
		return nil
	}
	return out.Events
}

func (a api) has(channel, event string) bool {
	for _, e := range a.history(channel) {
		if e.ID == event {
			return true
		}
	}
	return false
}

func (a api) kinds(channel string) string { return kinds(a.history(channel)) }
