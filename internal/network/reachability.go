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
)

const (
	msgPing     = "network.ping"
	msgDialBack = "network.dial_back"
)

type pingResponse struct {
	ID string `json:"id"`
}

type dialBackRequest struct {
	ID   string `json:"id"`
	Port int    `json:"port"`
}

type dialBackResponse struct {
	// Addr is where the peer observed the request from, with Port applied.
	Addr string `json:"addr"`
	// OK reports whether the peer reached the requesting node at Addr.
	OK bool `json:"ok"`
}

// Pong is the answer to a Ping.
type Pong struct {
	ID   string        // ID of the node that answered
	Addr string        // address it answered at
	RTT  time.Duration // round-trip time
}

// Ping pings the node at addrs, tried in order, and reports the first one
// that answers.
func (n *Network) Ping(ctx context.Context, addrs []string) (Pong, error) {
	var errs []error
	for _, addr := range addrs {
		start := time.Now()
		var resp pingResponse
		if err := n.Send(ctx, []string{addr}, msgPing, struct{}{}, &resp); err != nil {
			errs = append(errs, err)
			continue
		}
		return Pong{ID: resp.ID, Addr: addr, RTT: time.Since(start)}, nil
	}
	if len(errs) == 0 {
		return Pong{}, errors.New("no address")
	}
	return Pong{}, errors.Join(errs...)
}

func (n *Network) ping(context.Context, struct{}) (pingResponse, error) {
	return pingResponse{ID: n.cfg.ID}, nil
}

// dialBack tells the caller the address it was observed at and whether it
// accepts connections there. Only the caller's own IP is dialed, so this
// can't be used to make us connect to someone else.
func (n *Network) dialBack(ctx context.Context, req dialBackRequest) (dialBackResponse, error) {
	if req.ID == n.cfg.ID {
		// Reaching ourselves proves nothing, e.g. when mDNS finds our own
		// announcement.
		return dialBackResponse{}, errors.New("dial-back to self")
	}
	host, _, err := net.SplitHostPort(RemoteAddr(ctx))
	if err != nil {
		return dialBackResponse{}, err
	}
	addr := net.JoinHostPort(host, strconv.Itoa(req.Port))
	var resp pingResponse
	err = n.Send(ctx, []string{addr}, msgPing, struct{}{}, &resp)
	return dialBackResponse{Addr: addr, OK: err == nil && resp.ID == req.ID}, nil
}

// Run keeps the node's reachable addresses up to date until ctx is done.
// peers returns the addresses of known nodes to ask for dial-backs.
func (n *Network) Run(ctx context.Context, peers func(context.Context) [][]string) {
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
func (n *Network) checkReachability(ctx context.Context, peers [][]string) bool {
	rand.Shuffle(len(peers), func(i, j int) { peers[i], peers[j] = peers[j], peers[i] })

	var reachable, observed []string
	for _, peer := range peers[:min(3, len(peers))] {
		var resp dialBackResponse
		if err := n.Send(ctx, peer, msgDialBack, dialBackRequest{ID: n.cfg.ID, Port: n.cfg.ListenPort}, &resp); err != nil {
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
