package node

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go-decentralized/module"
)

// Handler is the node's local HTTP API, for tools such as the network
// explorer. Other nodes don't use it: they call over TLS. Calls through it
// are made as the node itself.
//
//	GET  /healthz                 liveness/readiness
//	GET  /v1/info                 this node, see node.info
//	POST /v1/capabilities/{ref}   calls a capability: the body is its input,
//	                              the response its result or an error
//	GET  /v1/events?ref=...       streams the events named, see serveEvents
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
		result, err := n.Call(r.Context(), ref, body)
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
	return mux
}

// keepAlive is how often an event stream says something, so that proxies
// don't take it for idle.
const keepAlive = 30 * time.Second

// serveEvents streams the events the query's refs name, as server-sent
// events: each has its ref as its type and its body as its data. The stream
// ends if the subscriber falls too far behind, see Subscription.Events.
func (n *Node) serveEvents(w http.ResponseWriter, r *http.Request) {
	sub, err := n.Subscribe(r.URL.Query()["ref"]...)
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
