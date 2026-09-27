package discovery

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"go-decentralized/modules/discovery/kademlia"
	"go-decentralized/modules/discovery/mdns"
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
func (m *Module) advertise(ctx context.Context, port int) {
	// DNS labels are limited to 63 bytes, so the instance is an ID prefix;
	// the full ID and the name go in TXT records.
	err := mdns.Advertise(ctx, mdns.Service{
		Type:     mdnsService,
		Instance: m.id.String()[:16],
		Port:     port,
		TXT:      []string{"id=" + m.id.String(), "name=" + m.env.NodeName},
	})
	if err != nil {
		slog.Warn("discovery: can't advertise on the local network", "err", err)
	}
}

// watchLAN keeps the peers found on the local network up to date.
func (m *Module) watchLAN(ctx context.Context) {
	for {
		m.browseLAN(ctx)
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
func (m *Module) browseLAN(ctx context.Context) []kademlia.Contact {
	found, err := mdns.Browse(ctx, mdnsService, time.Second)
	if err != nil {
		slog.Debug("discovery: can't browse the local network", "err", err)
	}
	lan := map[kademlia.ID]kademlia.Contact{}
	for _, e := range found {
		c := kademlia.Contact{Addrs: e.Addrs}
		for _, field := range e.TXT {
			if id, ok := strings.CutPrefix(field, "id="); ok {
				c.ID, _ = kademlia.ParseID(id)
			} else if name, ok := strings.CutPrefix(field, "name="); ok {
				c.Name = name
			}
		}
		if c.ID != m.id && c.ID != (kademlia.ID{}) {
			lan[c.ID] = c
		}
	}
	m.mu.Lock()
	m.lan = lan
	m.mu.Unlock()
	return slices.Collect(maps.Values(lan))
}

func (m *Module) lanPeers() []kademlia.Contact {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Collect(maps.Values(m.lan))
}

// findNode finds a node by ID. A peer on this node's LAN is tried at its LAN
// address first, then at the addresses it advertises.
func (m *Module) findNode(ctx context.Context, id kademlia.ID) (kademlia.Contact, bool, error) {
	found, ok, err := m.dht.FindNode(ctx, id)
	m.mu.Lock()
	lan, onLAN := m.lan[id]
	m.mu.Unlock()
	if !onLAN {
		return found, ok, err
	}
	if !ok {
		return lan, true, nil
	}
	others := slices.DeleteFunc(slices.Clone(found.Addrs), func(a string) bool { return slices.Contains(lan.Addrs, a) })
	found.Addrs = slices.Concat(lan.Addrs, others)
	return found, true, nil
}
