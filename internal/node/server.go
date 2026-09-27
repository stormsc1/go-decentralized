package node

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"go-decentralized/internal/api"
	"go-decentralized/internal/module"
)

// Handler is the node's local HTTP API, e.g. for the network explorer. Other
// nodes don't use it: they talk to the network, over TLS.
//
//	GET  /healthz                 liveness/readiness
//	GET  /v1/info                 node description (api.NodeInfo)
//	POST /v1/capabilities/{ref}   invoke "<module>.<capability>" with JSON args
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
		args := module.Args{}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
				writeJSON(w, http.StatusBadRequest, api.InvokeResponse{Error: "invalid args: " + err.Error()})
				return
			}
		}
		if _, err := n.Registry.Resolve(ref); err != nil {
			writeJSON(w, http.StatusNotFound, api.InvokeResponse{Error: err.Error()})
			return
		}
		result, err := n.Registry.Invoke(r.Context(), ref, args)
		if err != nil {
			slog.Warn("capability failed", "ref", ref, "err", err)
			writeJSON(w, http.StatusInternalServerError, api.InvokeResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, api.InvokeResponse{Result: result})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
