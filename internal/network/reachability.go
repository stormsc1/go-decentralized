package network

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"go-decentralized/module"
)

const (
	refPing     = "network.ping"
	refDialBack = "network.dial_back"
	// maxKnown bounds how many peers are kept to ask for dial-backs.
	maxKnown = 16
)

type dialBackInput struct {
	Port int `json:"port"`
}

type dialBackOutput struct {
	// Addr is where the peer saw the call come from, with Port applied.
	Addr string `json:"addr"`
	// Reachable reports whether the peer reached the caller at Addr.
	Reachable bool `json:"reachable"`
}

func (n *Network) ping(context.Context, struct{}) (struct{}, error) {
	return struct{}{}, nil
}

// dialBack tells the caller the address it was observed at and whether it
// accepts connections there, as it proves over TLS. Only the caller's own IP
// is dialed, so this can't be used to make us connect to someone else. It
// dials a new connection: the caller's session with us proves nothing.
func (n *Network) dialBack(ctx context.Context, in dialBackInput) (dialBackOutput, error) {
	caller := RemoteID(ctx)
	if caller == n.id {
		// Reaching ourselves proves nothing, e.g. when mDNS finds our own
		// announcement.
		return dialBackOutput{}, module.Errorf(module.CodeInvalidArgument, "dial-back to self")
	}
	host, _, err := net.SplitHostPort(RemoteAddr(ctx)) // fails for relay paths
	if err != nil {
		return dialBackOutput{}, module.Errorf(module.CodeInvalidArgument, "no address to dial back")
	}
	addr := net.JoinHostPort(host, strconv.Itoa(in.Port))
	s, err := n.openSession(ctx, addr, caller, false)
	if err == nil {
		_, err = n.call(ctx, s, refPing, nil)
		s.close()
	}
	return dialBackOutput{Addr: addr, Reachable: err == nil}, nil
}

// remember keeps a peer that answered at a direct address, to ask for
// dial-backs.
func (n *Network) remember(p module.Peer) {
	if p.ID == n.id {
		return // e.g. found on the LAN by mDNS: asking ourselves proves nothing
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.known = slices.DeleteFunc(n.known, func(k module.Peer) bool { return slices.Equal(k.Addrs, p.Addrs) })
	n.known = slices.Insert(n.known, 0, p)
	n.known = n.known[:min(maxKnown, len(n.known))]
}

// peers returns the peers to ask for dial-backs: those that answered at a
// public address, and so are outside any NAT this node is behind.
func (n *Network) peers() []module.Peer {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(n.known), func(p module.Peer) bool {
		return !n.cfg.Private && !public(p.Addrs[0])
	})
}

// watchReachability keeps the node's reachable addresses up to date until
// ctx is done, asking peers it has talked to for dial-backs.
func (n *Network) watchReachability(ctx context.Context) {
	if n.cfg.ListenPort == 0 {
		return
	}
	for {
		wait := time.Minute
		if !n.checkReachability(ctx, n.peers()) {
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
func (n *Network) checkReachability(ctx context.Context, peers []module.Peer) bool {
	rand.Shuffle(len(peers), func(i, j int) { peers[i], peers[j] = peers[j], peers[i] })

	var reachable, observed []string
	for _, peer := range peers[:min(3, len(peers))] {
		body, _ := json.Marshal(dialBackInput{Port: n.cfg.ListenPort})
		result, err := n.Call(ctx, peer, refDialBack, body)
		var resp dialBackOutput
		if err != nil || json.Unmarshal(result, &resp) != nil {
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
		if resp.Reachable && !slices.Contains(reachable, resp.Addr) && !slices.Contains(n.cfg.Announce, resp.Addr) {
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
