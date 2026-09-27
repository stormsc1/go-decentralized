package capabilities

import (
	"context"

	"go-decentralized/internal/module"
)

type ListReservations struct {
	Reservations func() []string
}

func (c *ListReservations) Name() string { return "list_reservations" }
func (c *ListReservations) Description() string {
	return "Lists the IDs of the nodes reachable through this relay."
}

func (c *ListReservations) Invoke(context.Context, module.Args) (any, error) {
	return c.Reservations(), nil
}
