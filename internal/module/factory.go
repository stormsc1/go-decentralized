package module

import (
	"context"
	"crypto/ed25519"
	"time"

	"go-decentralized/internal/api"
)

// Env is what a module gets from the node it runs in. The node owns the
// transport: a module sends messages through Send and handles them by
// implementing Receiver.
type Env struct {
	// NodeID uniquely identifies the hosting node (hex, see identity.NodeID).
	NodeID string
	// Key is the node's private key, which NodeID is derived from. Modules
	// use it to sign what they publish on the node's behalf.
	Key ed25519.PrivateKey
	// NodeName is the name from the node definition.
	NodeName string
	// Send delivers a message to another node.
	Send SendFunc
	// Network is a read-only view of the node's network.
	Network NetworkInfo
	// Registry holds every module of the node. It is populated as modules
	// are constructed, so only use it after construction.
	Registry *Registry
}

// NetworkInfo is what a module can see of the node's network.
type NetworkInfo interface {
	// Addrs returns every address the node can be reached at: direct ones
	// first, then through relays.
	Addrs() []string
	// DirectAddrs returns the addresses other nodes can connect to directly.
	// Nodes without any run in client mode.
	DirectAddrs() []string
	// Observed returns the public IPs other nodes see the node at.
	Observed() []string
	// ListenPort is the port the node accepts connections on, 0 if none.
	ListenPort() int
	// Traces returns what the node sent after since, oldest first.
	Traces(since time.Time) []api.Trace
	// Inspect reports the network's state, e.g. its relays, for debugging.
	Inspect() any
}

// Factory constructs a module. decode unmarshals the module's `config:` block
// from the node definition into a module specific struct.
type Factory func(decode func(v any) error, env Env) (Module, error)

// Runner is implemented by modules with background work. Run blocks until
// ctx is done.
type Runner interface {
	Run(ctx context.Context)
}

// Inspector is implemented by modules that report their internal state, as
// JSON-encodable data, for debugging (see the debug module).
type Inspector interface {
	Inspect() any
}

// PeerSource is implemented by modules that know other nodes. The node asks
// them for peers to check its own reachability with.
type PeerSource interface {
	Peers(ctx context.Context) []api.Peer
}
