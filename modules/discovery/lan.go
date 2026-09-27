package discovery

import (
	"context"
	"maps"
	"slices"
	"time"

	"go-decentralized/modules/discovery/kademlia"
)

// lanRefresh is how often the local network is browsed for peers.
const lanRefresh = 30 * time.Second

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

// browseLAN finds the nodes on the local network via mDNS and keeps them as
// LAN peers, which are reached directly at their LAN address even when they
// are behind a NAT and not in any routing table.
func (m *Module) browseLAN(ctx context.Context) []kademlia.Contact {
	found := browse(ctx)
	lan := map[kademlia.ID]kademlia.Contact{}
	for _, c := range found {
		if c.ID != m.id && c.ID != (kademlia.ID{}) {
			lan[c.ID] = c
		}
	}
	m.mu.Lock()
	m.lan = lan
	m.mu.Unlock()
	return found
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
