// Package mdns is a minimal multicast DNS service discovery (RFC 6762, 6763):
// enough to advertise a service instance on the local network, and to find
// the others.
package mdns

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	ttl        = 120 // seconds, for answers to multicast queries
	unicastTTL = 10  // seconds, for answers to one-shot queries (RFC 6762, 6.7)
)

var group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// Service is a service instance to advertise.
type Service struct {
	// Type is the service's type, e.g. "_http._tcp".
	Type string
	// Instance names the instance, uniquely on the network.
	Instance string
	Port     int
	TXT      []string
}

// Entry is a service instance found on the network.
type Entry struct {
	Instance string
	// Addrs are the instance's addresses, host:port.
	Addrs []string
	TXT   []string
}

// Advertise answers queries for s until ctx is done.
func Advertise(ctx context.Context, s Service) error {
	conn, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		return err
	}
	context.AfterFunc(ctx, func() { conn.Close() })
	service := s.Type + ".local."
	buf := make([]byte, 9000)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return nil
		}
		query, err := parse(buf[:n])
		if err != nil || query.reply || !slices.ContainsFunc(query.questions, func(q question) bool {
			return strings.EqualFold(q.name, service) && (q.typ == typePTR || q.typ == typeANY)
		}) {
			continue
		}
		// One-shot queriers, which don't send from port 5353, get their
		// answer directly, with their question and ID (RFC 6762, 6.7).
		reply, to := s.reply(ttl), group
		if from.Port != group.Port {
			reply, to = s.reply(unicastTTL), from
			reply.id, reply.questions = query.id, query.questions
		}
		_, _ = conn.WriteToUDP(reply.pack(), to)
	}
}

// reply describes s: its instance of the service, where it listens, and its
// TXT records.
func (s Service) reply(ttl uint32) message {
	service := s.Type + ".local."
	instance := s.Instance + "." + service
	host := s.Instance + ".local."
	r := message{
		reply:   true,
		answers: []record{{name: service, typ: typePTR, ttl: ttl, target: instance}},
		extra: []record{
			{name: instance, typ: typeSRV, ttl: ttl, target: host, port: uint16(s.Port)},
			{name: instance, typ: typeTXT, ttl: ttl, txt: s.TXT},
		},
	}
	for _, ip := range localIPs() {
		r.extra = append(r.extra, record{name: host, typ: typeA, ttl: ttl, ip: ip})
	}
	return r
}

// Browse asks the local network for instances of the service type typ, and
// returns those that answer within wait.
func Browse(ctx context.Context, typ string, wait time.Duration) ([]Entry, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	context.AfterFunc(ctx, func() { conn.Close() })
	service := typ + ".local."
	query := message{questions: []question{{name: service, typ: typePTR}}}
	if _, err := conn.WriteToUDP(query.pack(), group); err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	var found []Entry
	buf := make([]byte, 9000)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return found, nil
		}
		if reply, err := parse(buf[:n]); err == nil && reply.reply {
			found = append(found, entries(reply, service)...)
		}
	}
}

// entries returns the instances of service a reply describes.
func entries(m message, service string) []Entry {
	records := slices.Concat(m.answers, m.extra)
	var out []Entry
	for _, ptr := range records {
		if ptr.typ != typePTR || !strings.EqualFold(ptr.name, service) {
			continue
		}
		e := Entry{Instance: strings.TrimSuffix(ptr.target, "."+service)}
		var host string
		var port uint16
		for _, r := range records {
			switch {
			case r.typ == typeSRV && strings.EqualFold(r.name, ptr.target):
				host, port = r.target, r.port
			case r.typ == typeTXT && strings.EqualFold(r.name, ptr.target):
				e.TXT = r.txt
			}
		}
		for _, r := range records {
			if r.typ == typeA && r.ip != nil && port != 0 && strings.EqualFold(r.name, host) {
				e.Addrs = append(e.Addrs, net.JoinHostPort(r.ip.String(), strconv.Itoa(int(port))))
			}
		}
		if len(e.Addrs) > 0 {
			out = append(out, e)
		}
	}
	return out
}

// localIPs returns this machine's IPv4 addresses, other than loopback ones.
func localIPs() []net.IP {
	addrs, _ := net.InterfaceAddrs()
	var ips []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			ips = append(ips, n.IP.To4())
		}
	}
	return ips
}
