package discovery

import (
	"context"
	"io"
	"log"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/mdns"

	"go-decentralized/modules/discovery/kademlia"
)

// mdnsService is the DNS-SD service type nodes advertise on the local network.
const mdnsService = "_go-decentralized._tcp"

var quiet = log.New(io.Discard, "", 0)

// advertise answers mDNS queries for this node, listening on port, until stop
// is called.
func advertise(self kademlia.Contact, port int) (stop func(), err error) {
	// DNS labels are limited to 63 bytes, so the instance is an ID prefix;
	// the full ID and the name go in TXT records.
	txt := []string{"id=" + self.ID.String(), "name=" + self.Name}
	svc, err := mdns.NewMDNSService(self.ID.String()[:16], mdnsService, "", "", port, nil, txt)
	if err != nil {
		return nil, err
	}
	server, err := mdns.NewServer(&mdns.Config{Zone: svc, Logger: quiet})
	if err != nil {
		return nil, err
	}
	return func() { _ = server.Shutdown() }, nil
}

// browse returns the nodes advertising on the local network, with their
// LAN address.
func browse(ctx context.Context) []kademlia.Contact {
	entries := make(chan *mdns.ServiceEntry, 64)
	params := mdns.DefaultParams(mdnsService)
	params.Entries = entries
	params.Timeout = time.Second
	params.DisableIPv6 = true
	params.Logger = quiet
	if err := mdns.QueryContext(ctx, params); err != nil {
		slog.Debug("discovery: mdns query failed", "err", err)
	}
	close(entries)

	var found []kademlia.Contact
	for e := range entries {
		if e.AddrV4 == nil {
			continue
		}
		c := kademlia.Contact{Addrs: []string{net.JoinHostPort(e.AddrV4.String(), strconv.Itoa(e.Port))}}
		for _, field := range e.InfoFields {
			if id, ok := strings.CutPrefix(field, "id="); ok {
				c.ID, _ = kademlia.ParseID(id)
			} else if name, ok := strings.CutPrefix(field, "name="); ok {
				c.Name = name
			}
		}
		found = append(found, c)
	}
	return found
}
