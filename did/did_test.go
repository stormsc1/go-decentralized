package did

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBase58(t *testing.T) {
	for _, c := range []struct {
		data []byte
		enc  string
	}{
		{[]byte("Hello World!"), "2NEpo7TZRRrLZSi2U"},
		{[]byte{0, 0, 1}, "112"},
		{[]byte{0}, "1"},
		{nil, ""},
	} {
		if got := base58(c.data); got != c.enc {
			t.Errorf("base58(%x) = %q, want %q", c.data, got, c.enc)
		}
		if got, err := unbase58(c.enc); err != nil || !bytes.Equal(got, c.data) {
			t.Errorf("unbase58(%q) = %x, %v; want %x", c.enc, got, err, c.data)
		}
	}
	for n := range 100 {
		data := make([]byte, 1+n%40)
		rand.Read(data)
		data[0] %= 3 // sometimes leading zeros
		if got, err := unbase58(base58(data)); err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%x came back as %x, %v", data, got, err)
		}
	}
	if _, err := unbase58("0OIl"); err == nil {
		t.Fatal("decoded characters base58 leaves out")
	}
}

// The did:key spec's test vector: the Ed25519 key from 32 zero bytes.
func TestKey(t *testing.T) {
	pub := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	const want = "did:key:z6MkiTBz1ymuepAQ4HEHYSF1H8quG5GLVVQR3djdX3mDooWp"
	if got := Key(pub); got != want {
		t.Fatalf("Key = %s, want %s", got, want)
	}
	if got, err := PublicKey(want); err != nil || !got.Equal(pub) {
		t.Fatalf("PublicKey = %x, %v", got, err)
	}
	secp256k1 := "did:key:z" + base58(slices.Concat([]byte{0xe7, 0x01}, make([]byte, 33)))
	for _, bad := range []string{"did:web:example.com", "did:key:" + want[9:], want[:40], want + "1", secp256k1, prefix + strings.Repeat("2", 100)} {
		if _, err := PublicKey(bad); err == nil {
			t.Errorf("PublicKey(%q) took it", bad)
		}
	}
}

func TestSigned(t *testing.T) {
	now := time.Now()
	root, rootKey, _ := New()
	device, deviceKey, _ := New()
	_, otherKey, _ := New()

	s, err := Sign(rootKey, "chat.event", []byte(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if from, err := s.Verify("chat.event", now); err != nil || from != root {
		t.Fatalf("Verify = %s, %v; want %s", from, err, root)
	}
	if _, err := s.Verify("chat.other", now); err == nil {
		t.Fatal("verified for another purpose")
	}
	tampered := s
	tampered.Data = []byte(`{"text":"bye"}`)
	if _, err := tampered.Verify("chat.event", now); err == nil {
		t.Fatal("verified tampered data")
	}

	// Carried as JSON, and verified as is.
	delegation, err := Delegate(rootKey, device, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	s, err = SignAs(deviceKey, delegation, "chat.event", []byte(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(s)
	var carried Signed
	if err := json.Unmarshal(data, &carried); err != nil {
		t.Fatal(err)
	}
	if from, err := carried.Verify("chat.event", now); err != nil || from != root {
		t.Fatalf("Verify = %s, %v; want the root %s", from, err, root)
	}
	if _, err := carried.Verify("chat.event", now.Add(2*time.Hour)); err == nil {
		t.Fatal("verified after the delegation expired")
	}

	// A delegation only covers the device it names, signed by the root for
	// delegating.
	stolen, _ := SignAs(otherKey, delegation, "chat.event", []byte(`{}`))
	if _, err := stolen.Verify("chat.event", now); err == nil {
		t.Fatal("verified a key the delegation doesn't name")
	}
	notDelegation, _ := Sign(rootKey, "chat.event", delegation.Data)
	misused, _ := SignAs(deviceKey, notDelegation, "chat.event", []byte(`{}`))
	if _, err := misused.Verify("chat.event", now); err == nil {
		t.Fatal("verified a delegation signed for another purpose")
	}
	onward, _ := Delegate(deviceKey, Key(otherKey.Public().(ed25519.PublicKey)), now.Add(time.Hour))
	onward.Delegation = &delegation
	nested, _ := SignAs(otherKey, onward, "chat.event", []byte(`{}`))
	if _, err := nested.Verify("chat.event", now); err == nil {
		t.Fatal("verified a device's delegation")
	}
}
