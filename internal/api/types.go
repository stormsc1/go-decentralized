// Package api holds the wire types shared between nodes.
package api

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
