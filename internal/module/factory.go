package module

import (
	"context"
	"crypto/ed25519"

	"go-decentralized/internal/network"
)

// Env is the node context handed to a module when it is constructed.
type Env struct {
	// NodeID uniquely identifies the hosting node (hex, see identity.NodeID).
	NodeID string
	// Key is the node's private key, which NodeID is derived from. Modules
	// use it to sign what they publish on the node's behalf.
	Key ed25519.PrivateKey
	// NodeName is the name from the node definition.
	NodeName string
	// Network exchanges messages with other nodes and knows this node's
	// reachable addresses.
	Network *network.Network
	// Registry holds every module of the node. It is populated as modules
	// are constructed, so only use it after construction.
	Registry *Registry
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
