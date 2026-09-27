package network

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/coder/websocket"

	"go-decentralized/internal/noise"
	"go-decentralized/module"
)

// Sessions are secured inside the WebSocket, not by what carries it: a Noise
// XX handshake proves both nodes' keys and encrypts everything after, end to
// end, so TLS-ending platforms, proxies and relays only see ciphertext. See
// spec/wire.md, "Sessions".

// noisePurpose is what a node's key signs its Noise static key for, tying
// the handshake to its ID.
const noisePurpose = "network.noise"

// identity is a handshake payload: the sender's key, and its signature of
// the sender's Noise static key for noisePurpose.
type identity struct {
	Key []byte `json:"key"`
	Sig []byte `json:"sig"`
}

// secure runs the Noise handshake over ws, as the dialer (initiator) or the
// answerer, and returns the ID the other end proved and the secured stream.
// If expect is set, the other end must be that node.
func (n *Network) secure(ctx context.Context, ws *websocket.Conn, initiator bool, expect string) (string, *secured, error) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	write := func(message []byte, err error) error {
		if err != nil {
			return err
		}
		return ws.Write(ctx, websocket.MessageBinary, message)
	}
	read := func(hs *noise.Handshake) ([]byte, error) {
		kind, message, err := ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		if kind != websocket.MessageBinary {
			return nil, errors.New("handshake messages are binary")
		}
		return hs.ReadMessage(message)
	}

	// The prologue ties the handshake to the protocol version both ends
	// agreed on.
	hs := noise.NewHandshake(initiator, n.static, []byte(subprotocol))
	var payload []byte
	var err error
	if initiator {
		if err = write(hs.WriteMessage(nil)); err == nil {
			payload, err = read(hs)
			if err == nil {
				err = write(hs.WriteMessage(n.identity))
			}
		}
	} else {
		if _, err = read(hs); err == nil {
			if err = write(hs.WriteMessage(n.identity)); err == nil {
				payload, err = read(hs)
			}
		}
	}
	if err != nil {
		return "", nil, fmt.Errorf("handshake: %w", err)
	}
	peer, err := verifyIdentity(payload, hs.RemoteStatic().Bytes(), expect)
	if err != nil {
		return "", nil, err
	}
	send, receive, err := hs.Split()
	if err != nil {
		return "", nil, err
	}
	return peer, &secured{ws: ws, send: send, receive: receive}, nil
}

// verifyIdentity returns the ID of the node whose identity payload signs the
// Noise static key rs, which the handshake proved the other end holds.
func verifyIdentity(payload, rs []byte, expect string) (string, error) {
	var id identity
	if err := json.Unmarshal(payload, &id); err != nil {
		return "", fmt.Errorf("handshake: identity: %w", err)
	}
	if len(id.Key) != ed25519.PublicKeySize || !module.Verify(id.Key, noisePurpose, rs, id.Sig) {
		return "", errors.New("handshake: the identity doesn't sign the session's key")
	}
	peer := module.NodeID(id.Key)
	if expect != "" && peer != expect {
		return "", fmt.Errorf("reached node %.8s, not %.8s", peer, expect)
	}
	return peer, nil
}

// secured carries whole messages over a WebSocket, encrypted: each WebSocket
// message holds one or more Noise messages, length-prefixed, since a Noise
// message caps at 64 KiB.
type secured struct {
	ws            *websocket.Conn
	wmu           sync.Mutex // writers of notices may be concurrent
	send, receive *noise.Cipher
}

// chunk is the largest plaintext one Noise message carries.
const chunk = noise.MaxMessage - noise.Overhead

func (s *secured) Write(message []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	var out []byte
	for first := true; first || len(message) > 0; first = false {
		part := message[:min(chunk, len(message))]
		message = message[len(part):]
		out = binary.BigEndian.AppendUint16(out, uint16(len(part)+noise.Overhead))
		var err error
		if out, err = s.send.Encrypt(out, part); err != nil {
			return err
		}
	}
	return s.ws.Write(context.Background(), websocket.MessageBinary, out)
}

func (s *secured) Read() ([]byte, error) {
	kind, data, err := s.ws.Read(context.Background())
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageBinary {
		return nil, errors.New("messages are binary")
	}
	var message []byte
	for len(data) > 0 {
		if len(data) < 2 {
			return nil, errors.New("truncated message")
		}
		size := int(binary.BigEndian.Uint16(data))
		if len(data) < 2+size {
			return nil, errors.New("truncated message")
		}
		if message, err = s.receive.Decrypt(message, data[2:2+size]); err != nil {
			return nil, err
		}
		data = data[2+size:]
	}
	return message, nil
}

func (s *secured) Close() error { return s.ws.CloseNow() }

// Ping checks the other end of the WebSocket answers. Through a relay, the
// relay answers: liveness end to end comes from calls.
func (s *secured) Ping(ctx context.Context) error { return s.ws.Ping(ctx) }
