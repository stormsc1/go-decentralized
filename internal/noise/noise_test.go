package noise

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"testing"
)

// The Noise_XX_25519_AESGCM_SHA256 test vector from cacophony
// (https://github.com/haskell-cryptography/cacophony, vectors/cacophony.txt),
// also used by snow: three handshake messages, then transport messages both
// ways.
var vector = struct {
	prologue                  string
	initStatic, initEphemeral string
	respStatic, respEphemeral string
	handshakeHash             string
	payloads, messages        []string
}{
	prologue:      "4a6f686e2047616c74",
	initStatic:    "e61ef9919cde45dd5f82166404bd08e38bceb5dfdfded0a34c8df7ed542214d1",
	initEphemeral: "893e28b9dc6ca8d611ab664754b8ceb7bac5117349a4439a6b0569da977c464a",
	respStatic:    "4a3acbfdb163dec651dfa3194dece676d437029c62a408b4c5ea9114246e4893",
	respEphemeral: "bbdb4cdbd309f1a1f2e1456967fe288cadd6f712d65dc7b7793d5e63da6b375b",
	handshakeHash: "1b7aefb1125762aa21a252890d00af54519638b76437444538f9a52f21e2e0dc",
	payloads: []string{
		"4c756477696720766f6e204d69736573",
		"4d757272617920526f746862617264",
		"462e20412e20486179656b",
		"4361726c204d656e676572",
		"4a65616e2d426170746973746520536179",
		"457567656e2042f6686d20766f6e2042617765726b",
	},
	messages: []string{
		"ca35def5ae56cec33dc2036731ab14896bc4c75dbb07a61f879f8e3afa4c79444c756477696720766f6e204d69736573",
		"95ebc60d2b1fa672c1f46a8aa265ef51bfe38e7ccb39ec5be34069f144808843757117acceb05bd7a45733bc22015c97a9d0cbaf41b80446d5988ff5127235d76b79eade70f473d6a4ef521fdcbeda5340d01e028ba793fc059f2724a83af05f12dda0448a7621a926b379a92477fd",
		"c90f1cf77eba4e50edb038991565e36c9758943a989229b6051244dc4fbecb6946744b401af2ee1a5881b65fbb87fd07cb6a328ececc9ce6ce84c399dc332d4fd521fa4bb7f467ce909395",
		"bc3fa77f6aca3e8466d7dc6bea10013e88a6a29add5132b461806c",
		"250b01074cdfe0df2ecf8ccbf1737b15a2ddb5b52fd9a396604e9c793cee3b3bb9",
		"449d4d433b3cdc3d02bf6fc881774b9df54366ebcffb9689bb13f14709822cd7ef42bcdb4d",
	},
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func key(t *testing.T, seed string) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.X25519().NewPrivateKey(unhex(t, seed))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestVector runs both sides of the official test vector, so every
// ciphertext, the transcript hash and both transport directions are checked
// against another implementation's.
func TestVector(t *testing.T) {
	prologue := unhex(t, vector.prologue)
	init := NewHandshake(true, key(t, vector.initStatic), prologue)
	init.e = key(t, vector.initEphemeral)
	resp := NewHandshake(false, key(t, vector.respStatic), prologue)
	resp.e = key(t, vector.respEphemeral)

	// The handshake: the initiator writes messages 0 and 2.
	for i := range 3 {
		writer, reader := init, resp
		if i%2 == 1 {
			writer, reader = resp, init
		}
		payload := unhex(t, vector.payloads[i])
		message, err := writer.WriteMessage(payload)
		if err != nil || !bytes.Equal(message, unhex(t, vector.messages[i])) {
			t.Fatalf("message %d = %x, %v\nwant %s", i, message, err, vector.messages[i])
		}
		got, err := reader.ReadMessage(message)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("message %d carried %x, %v", i, got, err)
		}
	}
	if !bytes.Equal(init.Hash(), unhex(t, vector.handshakeHash)) || !bytes.Equal(init.Hash(), resp.Hash()) {
		t.Fatalf("handshake hash = %x, want %s", init.Hash(), vector.handshakeHash)
	}
	if !bytes.Equal(init.RemoteStatic().Bytes(), resp.s.PublicKey().Bytes()) ||
		!bytes.Equal(resp.RemoteStatic().Bytes(), init.s.PublicKey().Bytes()) {
		t.Fatal("the sides didn't learn each other's static keys")
	}

	// Transport messages, still alternating.
	initSend, initRecv, err := init.Split()
	if err != nil {
		t.Fatal(err)
	}
	respSend, respRecv, err := resp.Split()
	if err != nil {
		t.Fatal(err)
	}
	for i := 3; i < len(vector.payloads); i++ {
		send, recv := initSend, respRecv
		if i%2 == 1 {
			send, recv = respSend, initRecv
		}
		payload := unhex(t, vector.payloads[i])
		message, err := send.Encrypt(nil, payload)
		if err != nil || !bytes.Equal(message, unhex(t, vector.messages[i])) {
			t.Fatalf("message %d = %x, %v\nwant %s", i, message, err, vector.messages[i])
		}
		got, err := recv.Decrypt(nil, message)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("message %d carried %x, %v", i, got, err)
		}
	}
}

// handshake runs a whole handshake and returns both sides' ciphers.
func handshake(t *testing.T) (init, resp *Handshake, initSend, initRecv, respSend, respRecv *Cipher) {
	t.Helper()
	is, err := NewStatic()
	if err != nil {
		t.Fatal(err)
	}
	rs, err := NewStatic()
	if err != nil {
		t.Fatal(err)
	}
	init, resp = NewHandshake(true, is, nil), NewHandshake(false, rs, nil)
	for i := range 3 {
		writer, reader := init, resp
		if i%2 == 1 {
			writer, reader = resp, init
		}
		message, err := writer.WriteMessage(nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.ReadMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	initSend, initRecv, err = init.Split()
	if err != nil {
		t.Fatal(err)
	}
	respSend, respRecv, err = resp.Split()
	if err != nil {
		t.Fatal(err)
	}
	return init, resp, initSend, initRecv, respSend, respRecv
}

func TestTamperingFails(t *testing.T) {
	is, _ := NewStatic()
	rs, _ := NewStatic()
	init, resp := NewHandshake(true, is, nil), NewHandshake(false, rs, nil)
	m0, err := init.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resp.ReadMessage(m0); err != nil {
		t.Fatal(err)
	}
	m1, err := resp.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	m1[40] ^= 1 // inside the encrypted static key
	if _, err := init.ReadMessage(m1); err == nil {
		t.Fatal("took a tampered handshake message")
	}

	_, _, send, _, _, recv := handshake(t)
	message, err := send.Encrypt(nil, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Clone(message)
	tampered[0] ^= 1
	if _, err := recv.Decrypt(nil, tampered); err == nil {
		t.Fatal("took a tampered message")
	}
}

func TestProloguesMustMatch(t *testing.T) {
	is, _ := NewStatic()
	rs, _ := NewStatic()
	init, resp := NewHandshake(true, is, []byte("one")), NewHandshake(false, rs, []byte("two"))
	m0, _ := init.WriteMessage(nil)
	if _, err := resp.ReadMessage(m0); err != nil {
		t.Fatal(err) // nothing is keyed yet
	}
	m1, err := resp.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := init.ReadMessage(m1); err == nil {
		t.Fatal("the handshake survived different prologues")
	}
}

func TestMessagesCantBeReorderedOrReplayed(t *testing.T) {
	_, _, send, _, _, recv := handshake(t)
	var messages [][]byte
	for _, text := range []string{"one", "two"} {
		m, err := send.Encrypt(nil, []byte(text))
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	if _, err := recv.Decrypt(nil, messages[1]); err == nil {
		t.Fatal("took a message out of order")
	}
	// The failed try burned a nonce, ending the direction: even the right
	// message fails now.
	if _, err := recv.Decrypt(nil, messages[0]); err == nil {
		t.Fatal("took a message after a failure")
	}
}

func TestTurnsAreEnforced(t *testing.T) {
	is, _ := NewStatic()
	init := NewHandshake(true, is, nil)
	if _, err := init.ReadMessage(nil); err == nil {
		t.Fatal("the initiator read the first message")
	}
	if _, err := init.WriteMessage(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := init.WriteMessage(nil); err == nil {
		t.Fatal("the initiator wrote twice in a row")
	}
	if _, _, err := init.Split(); err == nil {
		t.Fatal("split before the handshake was done")
	}
}
