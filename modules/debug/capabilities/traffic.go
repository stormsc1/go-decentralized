package capabilities

import (
	"context"
	"time"

	"go-decentralized/internal/module"
)

type Traffic struct {
	Collect func(ctx context.Context, since time.Time) (any, error)
}

func (c *Traffic) Name() string { return "traffic" }
func (c *Traffic) Description() string {
	return "Lists the messages and streams sent by nodes running the debug module, oldest first (args: since=<RFC 3339 time>)."
}

func (c *Traffic) Invoke(ctx context.Context, args module.Args) (any, error) {
	var since time.Time
	if s := args.String("since"); s != "" {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return nil, err
		}
		since = t
	}
	return c.Collect(ctx, since)
}
