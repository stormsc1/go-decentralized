package network

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"go-decentralized/internal/api"
)

const (
	msgPing     = "network.ping"
	msgDialBack = "network.dial_back"
)

type dialBackRequest struct {
	Port int `json:"port"`
}

type dialBackResponse struct {
	// Addr is where the peer observed the request from, with Port applied.
	Addr string `json:"addr"`
	// OK reports whether the peer reached the requesting node at Addr.
	OK bool `json:"ok"`
}

func (n *Network) ping(context.Context, struct{}) (struct{}, error) {
	return struct{}{}, nil
}

// dialBack tells the caller the address it was observed at and whether it
// accepts connections there, as it proves over TLS. Only the caller's own IP
// is dialed, so this can't be used to make us connect to someone else.
func (n *Network) dialBack(ctx context.Context, req dialBackRequest) (dialBackResponse, error) {
	caller := RemoteID(ctx)
	if caller == n.id {
		// Reaching ourselves proves nothing, e.g. when mDNS finds our own
		// announcement.
		return dialBackResponse{}, errors.New("dial-back to self")
	}
	host, _, err := net.SplitHostPort(RemoteAddr(ctx))
	if err != nil {
		return dialBackResponse{}, err
	}
	addr := net.JoinHostPort(host, strconv.Itoa(req.Port))
	err = n.Send(ctx, api.Peer{ID: caller, Addrs: []string{addr}}, msgPing, struct{}{}, &struct{}{})
	return dialBackResponse{Addr: addr, OK: err == nil}, nil
}

// watchReachability keeps the node's reachable addresses up to date until
// ctx is done. peers returns known nodes to ask for dial-backs.
func (n *Network) watchReachability(ctx context.Context, peers func(context.Context) []api.Peer) {
	if n.cfg.ListenPort == 0 {
		return
	}
	for {
		wait := time.Minute
		if !n.checkReachability(ctx, peers(ctx)) {
			wait = 5 * time.Second // no usable answer yet
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// checkReachability asks a few peers to dial us back. It records the IPs they
// saw us at and keeps the addresses at least one of them reached. It reports
// false if no peer gave a usable answer, in which case nothing changes.
func (n *Network) checkReachability(ctx context.Context, peers []api.Peer) bool {
	rand.Shuffle(len(peers), func(i, j int) { peers[i], peers[j] = peers[j], peers[i] })

	var reachable, observed []string
	for _, peer := range peers[:min(3, len(peers))] {
		var resp dialBackResponse
		if err := n.Send(ctx, peer, msgDialBack, dialBackRequest{Port: n.cfg.ListenPort}, &resp); err != nil {
			continue
		}
		// A peer on our LAN sees and reaches our private address, which the
		// rest of the network can't, so only public addresses count.
		if !n.cfg.Private && !public(resp.Addr) {
			continue
		}
		if host, _, err := net.SplitHostPort(resp.Addr); err == nil && !slices.Contains(observed, host) {
			observed = append(observed, host)
		}
		if resp.OK && !slices.Contains(reachable, resp.Addr) && !slices.Contains(n.cfg.Announce, resp.Addr) {
			reachable = append(reachable, resp.Addr)
		}
	}
	if len(observed) == 0 {
		return false
	}
	slices.Sort(observed)

	n.mu.Lock()
	defer n.mu.Unlock()
	if !slices.Equal(n.reachable, reachable) {
		slog.Info("network: reachable addresses changed", "addrs", reachable)
	}
	n.reachable, n.observed = reachable, observed
	return true
}

var sharedSpace = netip.MustParsePrefix("100.64.0.0/10") // carrier-grade NAT

// public reports whether addr is globally routable, unlike private,
// loopback, link-local and carrier-grade NAT addresses, which only nearby
// peers can reach.
func public(addr string) bool {
	ap, err := netip.ParseAddrPort(addr)
	ip := ap.Addr().Unmap()
	return err == nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedSpace.Contains(ip)
}
