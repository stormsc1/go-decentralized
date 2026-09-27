package kademlia

import (
	"context"

	"go-decentralized/internal/api"
	"go-decentralized/internal/module"
)

// Messages exchanged between DHT nodes.
const (
	msgFindNode      = "find_node"
	msgFindProviders = "find_providers"
	msgAddProvider   = "add_provider"
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

// Handlers returns the DHT's message handlers, by message name.
func (d *DHT) Handlers() map[string]module.Handler {
	return map[string]module.Handler{
		msgFindNode: d.handler(func(req request) response {
			return response{Contacts: d.table.Closest(req.Target, K)}
		}),
		msgFindProviders: d.handler(func(req request) response {
			return response{Providers: d.providers.get(req.Target), Contacts: d.table.Closest(req.Target, K)}
		}),
		msgAddProvider: d.handler(func(req request) response {
			if req.Record != nil && req.Record.ID() == req.From.ID {
				d.providers.add(req.Target, *req.Record)
			}
			return response{}
		}),
	}
}

func (d *DHT) handler(h func(request) response) module.Handler {
	return module.HandlerFor(func(ctx context.Context, req request) (response, error) {
		// Senders prove their ID over TLS; a contact claiming another ID is
		// ignored.
		if req.From.ID.String() != module.Sender(ctx) {
			req.From = Contact{}
		}
		d.table.Add(req.From)
		resp := h(req)
		resp.From = d.self()
		return resp, nil
	})
}

// call sends a message to the node at to.
func (d *DHT) call(ctx context.Context, to api.Peer, name string, req request) (response, error) {
	req.From = d.self()
	var resp response
	if err := d.cfg.Send(ctx, to, name, req, &resp); err != nil {
		return resp, err
	}
	d.table.Add(resp.From)
	return resp, nil
}

func peerOf(c Contact) api.Peer {
	return api.Peer{ID: c.ID.String(), Addrs: c.Addrs}
}
