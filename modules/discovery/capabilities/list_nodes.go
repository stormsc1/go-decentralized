package capabilities

import (
	"context"

	"go-decentralized/internal/module"
	"go-decentralized/modules/discovery/kademlia"
)

type ListNodes struct {
	DHT *kademlia.DHT
}

func (c *ListNodes) Name() string { return "list_nodes" }
func (c *ListNodes) Description() string {
	return "Lists the nodes this node knows, not the whole network (see debug.map_network)."
}

func (c *ListNodes) Invoke(ctx context.Context, _ module.Args) (any, error) {
	return c.DHT.Nodes(ctx)
}
