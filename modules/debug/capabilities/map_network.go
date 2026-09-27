package capabilities

import (
	"context"

	"go-decentralized/internal/module"
)

type MapNetwork struct {
	Map func(ctx context.Context) (any, error)
}

func (c *MapNetwork) Name() string { return "map_network" }
func (c *MapNetwork) Description() string {
	return "Reports on every node running the debug module, found by capability. Sends a message to each."
}

func (c *MapNetwork) Invoke(ctx context.Context, _ module.Args) (any, error) {
	return c.Map(ctx)
}
