package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"go-decentralized/did"
	"go-decentralized/module"
)

// LoginPurpose is what a person's device signs to sign in to the local API.
const LoginPurpose = "node.login"

const (
	challengeFor = 5 * time.Minute
	sessionFor   = 24 * time.Hour
	sessionKey   = "session" // the cookie
)

// Handler is the node's local HTTP API, for tools such as the network
// explorer and the chat web app. Other nodes don't use it: they call over
// sessions. Calls through it are made as the node itself, and for the person
// signed in, if any, see module.User and spec/modules.md, "Signing in".
//
//	GET  /healthz                 liveness/readiness
//	GET  /v1/info                 this node, see node.info
//	POST /v1/capabilities/{ref}   calls a capability: the body is its input,
//	                              the response its result or an error
//	GET  /v1/events?ref=...       streams the events named, see serveEvents
//	GET  /v1/challenge            a challenge to sign, to sign in
//	POST /v1/login                signs in: the body is the signed challenge
//	GET  /v1/session              who is signed in
//	POST /v1/logout               signs out
func (n *Node) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, n.Info())
	})
	mux.HandleFunc("POST /v1/capabilities/{ref}", func(w http.ResponseWriter, r *http.Request) {
		ref := r.PathValue("ref")
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, module.MaxMessage))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, module.Errorf(module.CodeInvalidArgument, "%v", err))
			return
		}
		result, err := n.Call(n.sessions.context(r), ref, body)
		if err != nil {
			e := module.ErrorOf(err)
			if e.Code == module.CodeUnknown {
				slog.Warn("capability failed", "ref", ref, "err", err)
			}
			writeJSON(w, status(e.Code), e)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(result)
	})
	mux.HandleFunc("GET /v1/events", n.serveEvents)
	mux.HandleFunc("GET /v1/challenge", n.sessions.challenge)
	mux.HandleFunc("POST /v1/login", n.sessions.login)
	mux.HandleFunc("GET /v1/session", n.sessions.whoami)
	mux.HandleFunc("POST /v1/logout", n.sessions.logout)
	return mux
}

// sessions are the people signed in to the local API. A person's device
// proves it acts for them by signing a challenge, see spec/modules.md,
// "Signing in".
type sessions struct {
	node string

	mu         sync.Mutex
	challenges map[string]time.Time // outstanding, and when they expire
	open       map[string]session   // by token
}

type session struct {
	Person  string    `json:"person"`
	Device  string    `json:"device"`
	Expires time.Time `json:"expires"`
}

func newSessions(node string) *sessions {
	return &sessions{node: node, challenges: map[string]time.Time{}, open: map[string]session{}}
}

// token returns random bytes, in hex.
func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *sessions) challenge(w http.ResponseWriter, r *http.Request) {
	c := token()
	s.mu.Lock()
	now := time.Now()
	for c, expires := range s.challenges {
		if now.After(expires) {
			delete(s.challenges, c)
		}
	}
	s.challenges[c] = now.Add(challengeFor)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"challenge": c, "node": s.node})
}

// login takes the signed challenge: signed data {challenge, node}, for the
// purpose node.login, by the person's device with its delegation. It answers
// with the session, and sets its cookie.
func (s *sessions) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Signed did.Signed `json:"signed"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, module.Errorf(module.CodeInvalidArgument, "%v", err))
		return
	}
	var claim struct {
		Challenge string `json:"challenge"`
		Node      string `json:"node"`
	}
	if err := json.Unmarshal(in.Signed.Data, &claim); err != nil || claim.Node != s.node {
		writeJSON(w, http.StatusBadRequest, module.Errorf(module.CodeInvalidArgument, "sign {challenge, node} for this node"))
		return
	}
	s.mu.Lock()
	expires, ok := s.challenges[claim.Challenge]
	delete(s.challenges, claim.Challenge) // one use, whatever happens
	s.mu.Unlock()
	if !ok || time.Now().After(expires) {
		writeJSON(w, http.StatusBadRequest, module.Errorf(module.CodeInvalidArgument, "unknown or expired challenge"))
		return
	}
	person, err := in.Signed.Verify(LoginPurpose, time.Now())
	if err != nil {
		writeJSON(w, http.StatusForbidden, module.Errorf(module.CodePermissionDenied, "%v", err))
		return
	}
	sess := session{Person: person, Device: in.Signed.Signer, Expires: time.Now().Add(sessionFor)}
	t := token()
	s.mu.Lock()
	s.open[t] = sess
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionKey, Value: t, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionFor.Seconds())})
	writeJSON(w, http.StatusOK, sess)
}

// of returns the session a request carries, if it's signed in.
func (s *sessions) of(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionKey)
	if err != nil {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.open[c.Value]
	if ok && time.Now().After(sess.Expires) {
		delete(s.open, c.Value)
		return session{}, false
	}
	return sess, ok
}

// context returns the request's context, with the person signed in, if any.
func (s *sessions) context(r *http.Request) context.Context {
	if sess, ok := s.of(r); ok {
		return module.WithUser(r.Context(), sess.Person)
	}
	return r.Context()
}

func (s *sessions) whoami(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.of(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, module.Errorf(module.CodePermissionDenied, "not signed in"))
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *sessions) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionKey); err == nil {
		s.mu.Lock()
		delete(s.open, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionKey, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// keepAlive is how often an event stream says something, so that proxies
// don't take it for idle.
const keepAlive = 30 * time.Second

// serveEvents streams the events the query's refs name, as server-sent
// events: each has its ref as its type and its body as its data. Events for
// particular people reach only them, so the stream is for whoever signed in.
// It ends if the subscriber falls too far behind, see Subscription.Events.
func (n *Node) serveEvents(w http.ResponseWriter, r *http.Request) {
	sub, err := n.SubscribeAs(module.User(n.sessions.context(r)), r.URL.Query()["ref"]...)
	if err != nil {
		e := module.ErrorOf(err)
		writeJSON(w, status(e.Code), e)
		return
	}
	defer sub.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	keep := time.NewTicker(keepAlive)
	defer keep.Stop()
	for err = rc.Flush(); err == nil; err = rc.Flush() {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			_, err = io.WriteString(w, ":\n\n")
		case e, ok := <-sub.Events():
			if !ok {
				return
			}
			_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Ref, e.Body)
		}
		if err != nil {
			return
		}
	}
}

// status maps an error code to an HTTP status.
func status(code string) int {
	switch code {
	case module.CodeInvalidArgument:
		return http.StatusBadRequest
	case module.CodePermissionDenied:
		return http.StatusForbidden
	case module.CodeNotFound, module.CodeUnimplemented:
		return http.StatusNotFound
	case module.CodeUnavailable:
		return http.StatusServiceUnavailable
	case module.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	}
	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
