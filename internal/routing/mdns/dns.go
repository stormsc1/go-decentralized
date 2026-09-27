package mdns

import (
	"encoding/binary"
	"errors"
	"net"
	"slices"
	"strings"
)

// A minimal DNS message codec, for what multicast DNS service discovery
// needs (RFC 1035, 6762, 6763): questions, and A, PTR, SRV and TXT records.

const (
	typeA   = 1
	typePTR = 12
	typeTXT = 16
	typeSRV = 33
	typeANY = 255

	classIN   = 1
	flagReply = 0x8400 // a response, authoritative
)

type message struct {
	id        uint16
	reply     bool
	questions []question
	answers   []record
	extra     []record // additional records
}

type question struct {
	name string
	typ  uint16
}

type record struct {
	name string
	typ  uint16
	ttl  uint32
	// By type:
	target string   // PTR, SRV: a name
	port   uint16   // SRV
	txt    []string // TXT
	ip     net.IP   // A
}

var errMalformed = errors.New("malformed DNS message")

func (m message) pack() []byte {
	var flags uint16
	if m.reply {
		flags = flagReply
	}
	b := make([]byte, 12, 512)
	binary.BigEndian.PutUint16(b[0:], m.id)
	binary.BigEndian.PutUint16(b[2:], flags)
	binary.BigEndian.PutUint16(b[4:], uint16(len(m.questions)))
	binary.BigEndian.PutUint16(b[6:], uint16(len(m.answers)))
	binary.BigEndian.PutUint16(b[10:], uint16(len(m.extra)))
	for _, q := range m.questions {
		b = appendName(b, q.name)
		b = binary.BigEndian.AppendUint16(b, q.typ)
		b = binary.BigEndian.AppendUint16(b, classIN)
	}
	for _, r := range slices.Concat(m.answers, m.extra) {
		b = appendName(b, r.name)
		b = binary.BigEndian.AppendUint16(b, r.typ)
		b = binary.BigEndian.AppendUint16(b, classIN)
		b = binary.BigEndian.AppendUint32(b, r.ttl)
		var data []byte
		switch r.typ {
		case typeA:
			data = r.ip.To4()
		case typePTR:
			data = appendName(nil, r.target)
		case typeSRV:
			data = binary.BigEndian.AppendUint16(make([]byte, 4), r.port) // priority and weight 0
			data = appendName(data, r.target)
		case typeTXT:
			for _, s := range r.txt {
				data = append(append(data, byte(len(s))), s...)
			}
		}
		b = binary.BigEndian.AppendUint16(b, uint16(len(data)))
		b = append(b, data...)
	}
	return b
}

// appendName appends a name, e.g. "_x._tcp.local.", as DNS labels.
func appendName(b []byte, name string) []byte {
	for label := range strings.SplitSeq(strings.TrimSuffix(name, "."), ".") {
		if label != "" {
			b = append(append(b, byte(len(label))), label...)
		}
	}
	return append(b, 0)
}

func parse(b []byte) (message, error) {
	var m message
	if len(b) < 12 {
		return m, errMalformed
	}
	m.id = binary.BigEndian.Uint16(b)
	m.reply = b[2]&0x80 != 0
	counts := [4]int{}
	for i := range counts {
		counts[i] = int(binary.BigEndian.Uint16(b[4+2*i:]))
	}
	off := 12
	for range counts[0] {
		name, n, err := readName(b, off)
		if err != nil || n+4 > len(b) {
			return m, errMalformed
		}
		m.questions = append(m.questions, question{name: name, typ: binary.BigEndian.Uint16(b[n:])})
		off = n + 4
	}
	for i := range counts[1] + counts[2] + counts[3] {
		r, n, err := readRecord(b, off)
		if err != nil {
			return m, err
		}
		if i < counts[1] {
			m.answers = append(m.answers, r)
		} else {
			m.extra = append(m.extra, r)
		}
		off = n
	}
	return m, nil
}

func readRecord(b []byte, off int) (record, int, error) {
	var r record
	name, off, err := readName(b, off)
	if err != nil || off+10 > len(b) {
		return r, 0, errMalformed
	}
	r.name, r.typ, r.ttl = name, binary.BigEndian.Uint16(b[off:]), binary.BigEndian.Uint32(b[off+4:])
	size := int(binary.BigEndian.Uint16(b[off+8:]))
	start, end := off+10, off+10+size
	if end > len(b) {
		return r, 0, errMalformed
	}
	data := b[start:end]
	switch r.typ {
	case typeA:
		if size == 4 {
			r.ip = net.IP(append([]byte(nil), data...))
		}
	case typePTR:
		r.target, _, err = readName(b, start)
	case typeSRV:
		if size < 7 {
			return r, 0, errMalformed
		}
		r.port = binary.BigEndian.Uint16(data[4:])
		r.target, _, err = readName(b, start+6)
	case typeTXT:
		for len(data) > 0 && int(data[0]) < len(data) {
			r.txt = append(r.txt, string(data[1:1+data[0]]))
			data = data[1+data[0]:]
		}
	}
	return r, end, err
}

// readName reads the name at off, following compression pointers, and
// returns it with the offset after it.
func readName(b []byte, off int) (string, int, error) {
	var labels []string
	end := -1
	for jumps := 0; ; {
		if off >= len(b) {
			return "", 0, errMalformed
		}
		n := int(b[off])
		switch {
		case n == 0:
			if end < 0 {
				end = off + 1
			}
			return strings.Join(labels, ".") + ".", end, nil
		case n&0xC0 == 0xC0:
			if off+1 >= len(b) || jumps > 16 {
				return "", 0, errMalformed
			}
			if end < 0 {
				end = off + 2
			}
			off = int(binary.BigEndian.Uint16(b[off:]) & 0x3FFF)
			jumps++
		default:
			if off+1+n > len(b) {
				return "", 0, errMalformed
			}
			labels = append(labels, string(b[off+1:off+1+n]))
			off += 1 + n
		}
	}
}
