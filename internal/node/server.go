package node

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

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
	return mux
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
