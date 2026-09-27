package capabilities

import (
	"context"

	"go-decentralized/internal/module"
)

type Report struct {
	Build func() any
}

func (c *Report) Name() string { return "report" }
func (c *Report) Description() string {
	return "Reports this node's addresses, modules and their state."
}

func (c *Report) Invoke(context.Context, module.Args) (any, error) {
	return c.Build(), nil
}
