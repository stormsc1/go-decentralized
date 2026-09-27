// Package noise implements the Noise protocol's XX handshake
// (Noise_XX_25519_AESGCM_SHA256, https://noiseprotocol.org/noise.html),
// with which nodes prove their keys to each other and encrypt their
// sessions end to end, whoever carries them: TLS-ending platforms, proxies
// and relays only see ciphertext. See spec/wire.md, "Sessions".
package noise

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// protocol names the handshake pattern and primitives, and starts the
// handshake's transcript hash.
const protocol = "Noise_XX_25519_AESGCM_SHA256"

// MaxMessage is the size of the largest Noise message, encrypted.
const MaxMessage = 65535

// Overhead is what encrypting adds to a message: the AEAD tag.
const Overhead = 16

// tags is how many messages a cipher may seal: after 2^64-1 the nonce would
// repeat.
const tags = ^uint64(0)

// A Handshake is one side of an XX handshake:
//
//	-> e
//	<- e, ee, s, es
//	-> s, se
//
// The initiator writes the first message; each side alternates from there,
// carrying a payload of its own in each. After the third message, Split
// returns the session's ciphers.
type Handshake struct {
	initiator bool
	msg       int // messages so far
	s, e      *ecdh.PrivateKey
	rs, re    *ecdh.PublicKey
	ss        symmetric
}

// NewHandshake starts a handshake as the initiator or responder, proving
// the static key s. Both sides must use the same prologue, or the handshake
// fails; it may be nil.
func NewHandshake(initiator bool, s *ecdh.PrivateKey, prologue []byte) *Handshake {
	h := &Handshake{initiator: initiator, s: s}
	h.ss.init()
	h.ss.mixHash(prologue)
	return h
}

// NewStatic makes a static key.
func NewStatic() (*ecdh.PrivateKey, error) { return ecdh.X25519().GenerateKey(rand.Reader) }

// WriteMessage returns the next handshake message, carrying payload. The
// first message's payload isn't encrypted; later ones are.
func (h *Handshake) WriteMessage(payload []byte) ([]byte, error) {
	if h.msg >= 3 || h.initiator != (h.msg%2 == 0) {
		return nil, errors.New("noise: not this side's turn")
	}
	var out []byte
	newEphemeral := func() error {
		if h.e == nil { // tests fix the ephemeral
			var err error
			if h.e, err = ecdh.X25519().GenerateKey(rand.Reader); err != nil {
				return err
			}
		}
		out = append(out, h.e.PublicKey().Bytes()...)
		h.ss.mixHash(h.e.PublicKey().Bytes())
		return nil
	}
	switch h.msg {
	case 0: // -> e
		if err := newEphemeral(); err != nil {
			return nil, err
		}
	case 1: // <- e, ee, s, es
		if err := newEphemeral(); err != nil {
			return nil, err
		}
		if err := h.mixDH(h.e, h.re); err != nil { // ee
			return nil, err
		}
		out = append(out, h.ss.encryptAndHash(h.s.PublicKey().Bytes())...)
		if err := h.mixDH(h.s, h.re); err != nil { // es
			return nil, err
		}
	case 2: // -> s, se
		out = append(out, h.ss.encryptAndHash(h.s.PublicKey().Bytes())...)
		if err := h.mixDH(h.s, h.re); err != nil { // se
			return nil, err
		}
	}
	h.msg++
	if len(out)+len(payload)+Overhead > MaxMessage {
		return nil, errors.New("noise: payload too large")
	}
	return append(out, h.ss.encryptAndHash(payload)...), nil
}

// ReadMessage takes the next handshake message and returns its payload.
func (h *Handshake) ReadMessage(message []byte) (_ []byte, err error) {
	if h.msg >= 3 || h.initiator == (h.msg%2 == 0) {
		return nil, errors.New("noise: not the other side's turn")
	}
	if len(message) > MaxMessage {
		return nil, errors.New("noise: message too large")
	}
	take := func(n int) ([]byte, error) {
		if len(message) < n {
			return nil, errors.New("noise: message too short")
		}
		part := message[:n]
		message = message[n:]
		return part, nil
	}
	readEphemeral := func() error {
		pub, err := take(32)
		if err != nil {
			return err
		}
		if h.re, err = ecdh.X25519().NewPublicKey(pub); err != nil {
			return err
		}
		h.ss.mixHash(pub)
		return nil
	}
	readStatic := func() error {
		enc, err := take(32 + h.ss.c.overhead())
		if err != nil {
			return err
		}
		pub, err := h.ss.decryptAndHash(enc)
		if err != nil {
			return err
		}
		h.rs, err = ecdh.X25519().NewPublicKey(pub)
		return err
	}
	switch h.msg {
	case 0: // -> e
		if err := readEphemeral(); err != nil {
			return nil, err
		}
	case 1: // <- e, ee, s, es
		if err := readEphemeral(); err != nil {
			return nil, err
		}
		if err := h.mixDH(h.e, h.re); err != nil { // ee
			return nil, err
		}
		if err := readStatic(); err != nil {
			return nil, err
		}
		if err := h.mixDH(h.e, h.rs); err != nil { // es
			return nil, err
		}
	case 2: // -> s, se
		if err := readStatic(); err != nil {
			return nil, err
		}
		if err := h.mixDH(h.e, h.rs); err != nil { // se
			return nil, err
		}
	}
	h.msg++
	return h.ss.decryptAndHash(message)
}

// mixDH mixes the shared secret of our key a and the other side's b into
// the handshake's keys.
func (h *Handshake) mixDH(a *ecdh.PrivateKey, b *ecdh.PublicKey) error {
	shared, err := a.ECDH(b)
	if err != nil {
		return fmt.Errorf("noise: %w", err)
	}
	h.ss.mixKey(shared)
	return nil
}

// Split returns the session's ciphers, once the handshake is done: one for
// what this side sends, one for what it receives.
func (h *Handshake) Split() (send, receive *Cipher, err error) {
	if h.msg != 3 {
		return nil, nil, errors.New("noise: the handshake isn't done")
	}
	one, two := hkdf(h.ss.ck, nil)
	initiator, responder := &Cipher{key: one}, &Cipher{key: two}
	if h.initiator {
		return initiator, responder, nil
	}
	return responder, initiator, nil
}

// RemoteStatic returns the static key the other side proved.
func (h *Handshake) RemoteStatic() *ecdh.PublicKey { return h.rs }

// Hash returns the handshake's transcript hash, which uniquely names the
// session for both sides: a channel binding.
func (h *Handshake) Hash() []byte { return h.ss.h[:] }

// Cipher carries one direction of a session.
type Cipher struct {
	key [32]byte
	n   uint64
}

// Encrypt seals plaintext as the next message of this direction, appending
// it to out.
func (c *Cipher) Encrypt(out, plaintext []byte) ([]byte, error) {
	if len(plaintext)+Overhead > MaxMessage {
		return nil, errors.New("noise: message too large")
	}
	if c.n == tags {
		return nil, errors.New("noise: the session is exhausted")
	}
	return c.aead().Seal(out, c.nonce(), plaintext, nil), nil
}

// Decrypt opens the next message of this direction. A lost or reordered
// message fails, and the session is over.
func (c *Cipher) Decrypt(out, message []byte) ([]byte, error) {
	if len(message) > MaxMessage {
		return nil, errors.New("noise: message too large")
	}
	if c.n == tags {
		return nil, errors.New("noise: the session is exhausted")
	}
	plaintext, err := c.aead().Open(out, c.nonce(), message, nil)
	if err != nil {
		return nil, errors.New("noise: bad message")
	}
	return plaintext, nil
}

// nonce returns the next nonce: 32 bits of zeros, then the count, big-endian
// as AESGCM's are.
func (c *Cipher) nonce() []byte {
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], c.n)
	c.n++
	return nonce[:]
}

func (c *Cipher) aead() cipher.AEAD {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		panic(err) // the key is always 32 bytes
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead
}

// symmetric is the handshake's symmetric state: the chaining key ck feeding
// every encryption key, and the transcript hash h binding every byte
// exchanged.
type symmetric struct {
	ck, h [32]byte
	c     handshakeCipher
}

func (s *symmetric) init() {
	copy(s.h[:], protocol) // shorter than the hash, so padded with zeros
	s.ck = s.h
}

func (s *symmetric) mixHash(data []byte) {
	hash := sha256.New()
	hash.Write(s.h[:])
	hash.Write(data)
	hash.Sum(s.h[:0])
}

func (s *symmetric) mixKey(ikm []byte) {
	var key [32]byte
	s.ck, key = hkdf(s.ck, ikm)
	s.c = handshakeCipher{c: Cipher{key: key}, keyed: true}
}

func (s *symmetric) encryptAndHash(plaintext []byte) []byte {
	out := s.c.encrypt(s.h[:], plaintext)
	s.mixHash(out)
	return out
}

func (s *symmetric) decryptAndHash(message []byte) ([]byte, error) {
	plaintext, err := s.c.decrypt(s.h[:], message)
	if err != nil {
		return nil, err
	}
	s.mixHash(message)
	return plaintext, nil
}

// handshakeCipher encrypts during the handshake: not at all before the
// first shared secret, and binding the transcript hash after.
type handshakeCipher struct {
	c     Cipher
	keyed bool
}

func (c *handshakeCipher) overhead() int {
	if !c.keyed {
		return 0
	}
	return Overhead
}

func (c *handshakeCipher) encrypt(h, plaintext []byte) []byte {
	if !c.keyed {
		return plaintext
	}
	return c.c.aead().Seal(nil, c.c.nonce(), plaintext, h)
}

func (c *handshakeCipher) decrypt(h, message []byte) ([]byte, error) {
	if !c.keyed {
		return message, nil
	}
	plaintext, err := c.c.aead().Open(nil, c.c.nonce(), message, h)
	if err != nil {
		return nil, errors.New("noise: bad handshake message")
	}
	return plaintext, nil
}

// hkdf derives two keys from the chaining key and new material, as the
// Noise spec defines it, on HMAC-SHA256.
func hkdf(ck [32]byte, ikm []byte) (one, two [32]byte) {
	mac := hmac.New(sha256.New, ck[:])
	mac.Write(ikm)
	temp := mac.Sum(nil)

	mac = hmac.New(sha256.New, temp)
	mac.Write([]byte{1})
	mac.Sum(one[:0])

	mac.Reset()
	mac.Write(one[:])
	mac.Write([]byte{2})
	mac.Sum(two[:0])
	return one, two
}
