package kademlia

import (
	"context"

	"go-decentralized/internal/network"
)

// Messages exchanged between DHT nodes.
const (
	msgFindNode      = "kademlia.find_node"
	msgFindProviders = "kademlia.find_providers"
	msgAddProvider   = "kademlia.add_provider"
)

// Every message carries the sender's contact, so nodes learn about each other
// simply by talking.
type request struct {
	From   Contact `json:"from"`
	Target ID      `json:"target"`
	// Record is the sender's record, announced by add_provider.
	Record *Record `json:"record,omitempty"`
}

type response struct {
	From      Contact   `json:"from"`
	Contacts  []Contact `json:"contacts,omitempty"`
	Providers []Record  `json:"providers,omitempty"`
}

func (d *DHT) registerHandlers() {
	d.handle(msgFindNode, func(req request) response {
		return response{Contacts: d.table.Closest(req.Target, K)}
	})
	d.handle(msgFindProviders, func(req request) response {
		return response{Providers: d.providers.get(req.Target), Contacts: d.table.Closest(req.Target, K)}
	})
	d.handle(msgAddProvider, func(req request) response {
		if req.Record != nil && req.Record.ID() == req.From.ID {
			d.providers.add(req.Target, *req.Record)
		}
		return response{}
	})
}

func (d *DHT) handle(name string, h func(request) response) {
	network.Handle(d.cfg.Messenger, name, func(_ context.Context, req request) (response, error) {
		d.table.Add(req.From)
		resp := h(req)
		resp.From = d.self()
		return resp, nil
	})
}

// call sends a message to the node reachable at addrs.
func (d *DHT) call(ctx context.Context, addrs []string, name string, req request) (response, error) {
	req.From = d.self()
	var resp response
	if err := d.cfg.Messenger.Send(ctx, addrs, name, req, &resp); err != nil {
		return resp, err
	}
	d.table.Add(resp.From)
	return resp, nil
}
