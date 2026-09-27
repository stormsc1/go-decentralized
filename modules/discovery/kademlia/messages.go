package kademlia

import (
	"context"

	"go-decentralized/module"
)

// The DHT's capabilities, which DHT nodes call on each other. See the
// discovery module's module.yaml.
const (
	capFindNode      = "dht_find_node"
	capFindProviders = "dht_find_providers"
	capAddProvider   = "dht_add_provider"
)

// Every call carries the caller's contact, and every result the callee's,
// so nodes learn about each other simply by talking.
type request struct {
	From   Contact `json:"from"`
	Target ID      `json:"target"`
	// Record is the caller's record, announced by dht_add_provider.
	Record *Record `json:"record,omitempty"`
}

type response struct {
	From      Contact   `json:"from"`
	Contacts  []Contact `json:"contacts,omitempty"`
	Providers []Record  `json:"providers,omitempty"`
}

// Handlers handle the DHT's capabilities, by name.
func (d *DHT) Handlers() map[string]module.Handler {
	return map[string]module.Handler{
		capFindNode: d.handler(func(req request) response {
			return response{Contacts: d.table.Closest(req.Target, K)}
		}),
		capFindProviders: d.handler(func(req request) response {
			return response{Providers: d.providers.get(req.Target), Contacts: d.table.Closest(req.Target, K)}
		}),
		capAddProvider: d.handler(func(req request) response {
			if req.Record == nil {
				return response{}
			}
			if r, ok := req.Record.verified(d.providers.ttl); ok && r.ID() == req.From.ID {
				d.providers.add(req.Target, r)
			}
			return response{}
		}),
	}
}

func (d *DHT) handler(h func(request) response) module.Handler {
	return module.HandlerFor(func(ctx context.Context, req request) (response, error) {
		// Callers prove their ID over TLS; a contact claiming another ID is
		// ignored.
		if req.From.ID.String() != module.Caller(ctx) {
			req.From = Contact{}
		}
		d.table.Add(req.From)
		resp := h(req)
		resp.From = d.self()
		return resp, nil
	})
}

// call calls one of the DHT's capabilities on the node at to.
func (d *DHT) call(ctx context.Context, to module.Peer, name string, req request) (response, error) {
	req.From = d.self()
	var resp response
	if err := d.cfg.Call(ctx, to, name, req, &resp); err != nil {
		return resp, err
	}
	// The callee proved its ID if we asked for one; a contact claiming
	// another is ignored.
	if to.ID == "" || resp.From.ID.String() == to.ID {
		d.table.Add(resp.From)
	}
	return resp, nil
}

func peerOf(c Contact) module.Peer {
	return module.Peer{ID: c.ID.String(), Addrs: c.Addrs}
}
