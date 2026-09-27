package node

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"go-decentralized/internal/api"
	"go-decentralized/internal/module"
	"go-decentralized/internal/network"
)

// Handler exposes the node over HTTP:
//
//	GET  /healthz                 liveness/readiness
//	GET  /v1/info                 node description (api.NodeInfo)
//	POST /v1/capabilities/{ref}   invoke "<module>.<capability>" with JSON args
//	POST /v1/messages/{name}      messages from other nodes (see network)
//	POST /v1/streams/{name}       streams from other nodes (see network)
func (n *Node) Handler() http.Handler {
	mux := http.NewServeMux()
	peers := n.Env.Network.Handler()
	mux.Handle(network.MessagesPath, peers)
	mux.Handle(network.StreamsPath, peers)
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
