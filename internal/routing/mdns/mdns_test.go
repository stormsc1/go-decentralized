package mdns

import (
	"encoding/binary"
	"net"
	"slices"
	"testing"
)

const service = "_test._tcp.local."

func TestRepliesDescribeTheInstance(t *testing.T) {
	s := Service{Type: "_test._tcp", Instance: "abcdef", Port: 8443, TXT: []string{"id=abcdef", "name=studio-node"}}
	reply := s.reply(ttl)
	reply.extra = append(reply.extra, record{name: "abcdef.local.", typ: typeA, ttl: ttl, ip: net.IPv4(192, 168, 1, 5)})

	parsed, err := parse(reply.pack())
	if err != nil {
		t.Fatal(err)
	}
	found := entries(parsed, service)
	if len(found) != 1 || found[0].Instance != "abcdef" || !slices.Equal(found[0].TXT, s.TXT) || !slices.Contains(found[0].Addrs, "192.168.1.5:8443") {
		t.Fatalf("entries = %+v", found)
	}
}

func TestParsesCompressedNames(t *testing.T) {
	// A question for the service, then a PTR answer whose name, and part of
	// whose target, point back at the question's name, at offset 12.
	b := message{questions: []question{{name: service, typ: typePTR}}}.pack()
	binary.BigEndian.PutUint16(b[6:], 1) // one answer
	b = binary.BigEndian.AppendUint16(b, 0xC00C)
	b = binary.BigEndian.AppendUint16(b, typePTR)
	b = binary.BigEndian.AppendUint16(b, classIN)
	b = binary.BigEndian.AppendUint32(b, ttl)
	target := append([]byte{3, 'a', 'b', 'c'}, 0xC0, 0x0C)
	b = binary.BigEndian.AppendUint16(b, uint16(len(target)))
	b = append(b, target...)

	m, err := parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.answers) != 1 || m.answers[0].name != service || m.answers[0].target != "abc."+service {
		t.Fatalf("answers = %+v", m.answers)
	}
}

func TestRejectsLoopingNames(t *testing.T) {
	b := message{}.pack()
	binary.BigEndian.PutUint16(b[4:], 1) // one question, whose name points at itself
	b = append(b, 0xC0, 12, 0, typePTR, 0, classIN)
	if _, err := parse(b); err == nil {
		t.Fatal("parsed a name that points at itself")
	}
}
