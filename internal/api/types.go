// Package api holds the types shared between nodes, and between a node and
// its modules.
package api

import "time"

// Peer is a node to send to: its ID, which the connection must prove if
// set, and addresses to try in order.
type Peer struct {
	ID    string   `json:"id,omitempty"`
	Addrs []string `json:"addrs"`
}

// Trace records a message or stream a node sent, for debugging.
type Trace struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"` // "message" or "stream"
	Name string    `json:"name"`
	From string    `json:"from"`
	// To is the ID of the node that answered, as proven over TLS.
	To       string        `json:"to,omitempty"`
	Addr     string        `json:"addr"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

// NodeInfo describes a node as reported by its /v1/info endpoint.
type NodeInfo struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Addrs       []string     `json:"addrs,omitempty"`
	Modules     []ModuleInfo `json:"modules"`
}

type ModuleInfo struct {
	Name         string           `json:"name"`
	Capabilities []CapabilityInfo `json:"capabilities"`
}

type CapabilityInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// InvokeResponse is the body returned by POST /v1/capabilities/{ref}.
type InvokeResponse struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}
