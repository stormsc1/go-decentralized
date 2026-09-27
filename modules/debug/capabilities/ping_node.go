package capabilities

import (
	"context"
	"errors"

	"go-decentralized/internal/module"
)

type PingNode struct {
	Ping func(ctx context.Context, id string) (any, error)
}

func (c *PingNode) Name() string { return "ping_node" }
func (c *PingNode) Description() string {
	return "Pings the node with the given ID, through a relay if needed (args: id=<hex>)."
}

func (c *PingNode) Invoke(ctx context.Context, args module.Args) (any, error) {
	id := args.String("id")
	if id == "" {
		return nil, errors.New("missing argument: id")
	}
	return c.Ping(ctx, id)
}
