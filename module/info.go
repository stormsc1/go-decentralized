package module

// NodeInfo describes a node, as its capability node.info returns it.
type NodeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// PublicKey is the node's Ed25519 public key, see NodeID.
	PublicKey []byte `json:"public_key"`
	// Addrs are the addresses the node can be reached at: direct ones first,
	// then through relays.
	Addrs []string `json:"addrs,omitempty"`
	// DirectAddrs are the addresses other nodes can connect to directly.
	// Nodes without any are in client mode.
	DirectAddrs []string `json:"direct_addrs,omitempty"`
	// Observed are the public IPs other nodes see the node at.
	Observed []string `json:"observed,omitempty"`
	// ListenPort is the port the node accepts connections on, 0 if none.
	ListenPort int          `json:"listen_port,omitempty"`
	Modules    []ModuleInfo `json:"modules"`
}

type ModuleInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	// Runtime is "builtin" for the node's own, "native" for modules compiled
	// into it and "process" for modules in a process of their own.
	Runtime      string           `json:"runtime"`
	Capabilities []CapabilityInfo `json:"capabilities,omitempty"`
	Events       []EventInfo      `json:"events,omitempty"`
}

type EventInfo struct {
	// Ref names the event: "<module>.<event>".
	Ref         string `json:"ref"`
	Description string `json:"description,omitempty"`
}

type CapabilityInfo struct {
	// Ref names the capability: "<module>.<capability>".
	Ref         string `json:"ref"`
	Description string `json:"description,omitempty"`
	Access      Access `json:"access"`
	Internal    bool   `json:"internal,omitempty"`
}
