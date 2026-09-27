package capabilities

import (
	"context"
	"errors"

	"go-decentralized/internal/module"
	"go-decentralized/modules/discovery/kademlia"
)

type FindCapabilityProviders struct {
	DHT *kademlia.DHT
}

func (c *FindCapabilityProviders) Name() string { return "find_capability_providers" }
func (c *FindCapabilityProviders) Description() string {
	return "Finds all nodes that provide a capability (args: capability=<module>.<capability>)."
}

func (c *FindCapabilityProviders) Invoke(ctx context.Context, args module.Args) (any, error) {
	capability := args.String("capability")
	if capability == "" {
		return nil, errors.New("missing argument: capability")
	}
	return c.DHT.FindProviders(ctx, capability)
}
