package capabilities

import (
	"context"
	"fmt"

	"go-decentralized/internal/module"
	"go-decentralized/modules/discovery/kademlia"
)

type FindNodeByID struct {
	Find func(ctx context.Context, id kademlia.ID) (kademlia.Contact, bool, error)
}

func (c *FindNodeByID) Name() string { return "find_node_by_id" }
func (c *FindNodeByID) Description() string {
	return "Finds the node with the given ID (args: id=<hex>)."
}

func (c *FindNodeByID) Invoke(ctx context.Context, args module.Args) (any, error) {
	id, err := kademlia.ParseID(args.String("id"))
	if err != nil {
		return nil, err
	}
	node, ok, err := c.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("node %s not found", id)
	}
	return node, nil
}
