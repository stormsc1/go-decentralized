package routing

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"go-decentralized/internal/routing/kademlia"
	"go-decentralized/internal/routing/mdns"
)

// Nodes advertise themselves on the local network with multicast DNS, as an
// instance of mdnsService, and browse for the others every lanRefresh. A
// studio's nodes find each other with no configuration, even without
// internet.
const (
	mdnsService = "_go-decentralized._tcp"
	lanRefresh  = 30 * time.Second
)

// advertise answers mDNS queries for this node, which listens on port, until
// ctx is done.
func (r *Routing) advertise(ctx context.Context, port int) {
	// DNS labels are limited to 63 bytes, so the instance is an ID prefix;
	// the full ID and the name go in TXT records.
	err := mdns.Advertise(ctx, mdns.Service{
		Type:     mdnsService,
		Instance: r.id.String()[:16],
		Port:     port,
		TXT:      []string{"id=" + r.id.String(), "name=" + r.cfg.Name, "scheme=" + r.cfg.Network.Scheme()},
	})
	if err != nil {
		slog.Warn("routing: can't advertise on the local network", "err", err)
	}
}

// watchLAN keeps the peers found on the local network up to date.
func (r *Routing) watchLAN(ctx context.Context) {
	for {
		r.browseLAN(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(lanRefresh):
		}
	}
}

// browseLAN finds the other nodes on the local network and keeps them as LAN
// peers, which are reached directly at their LAN address even when they are
// behind a NAT and in no routing table.
func (r *Routing) browseLAN(ctx context.Context) []kademlia.Contact {
	found, err := mdns.Browse(ctx, mdnsService, time.Second)
	if err != nil {
		slog.Debug("routing: can't browse the local network", "err", err)
	}
	lan := map[kademlia.ID]kademlia.Contact{}
	for _, e := range found {
		c := kademlia.Contact{}
		scheme := "wss"
		for _, field := range e.TXT {
			if id, ok := strings.CutPrefix(field, "id="); ok {
				c.ID, _ = kademlia.ParseID(id)
			} else if name, ok := strings.CutPrefix(field, "name="); ok {
				c.Name = name
			} else if s, ok := strings.CutPrefix(field, "scheme="); ok {
				scheme = s
			}
		}
		for _, hp := range e.Addrs {
			c.Addrs = append(c.Addrs, scheme+"://"+hp)
		}
		if c.ID != r.id && c.ID != (kademlia.ID{}) {
			lan[c.ID] = c
		}
	}
	r.mu.Lock()
	r.lan = lan
	r.mu.Unlock()
	return slices.Collect(maps.Values(lan))
}

func (r *Routing) lanPeers() []kademlia.Contact {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Collect(maps.Values(r.lan))
}

func (r *Routing) lanPeer(id kademlia.ID) (kademlia.Contact, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.lan[id]
	return c, ok
}
