package did

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go-decentralized/module"
)

// Signed is data an identity signed: with its own key, or with a device key
// it delegated to. The signature covers Data as it's carried, so verifying
// never re-encodes anything.
type Signed struct {
	Data []byte `json:"data"`
	// Signer is the did:key of the key that signed.
	Signer string `json:"signer"`
	// Delegation is set if Signer is a device: the identity's delegation to
	// it.
	Delegation *Signed `json:"delegation,omitempty"`
	Sig        []byte  `json:"sig"`
}

// Sign signs data for purpose with key, an identity's own. Purposes keep
// signatures made for one thing from passing for another, as in
// module.Sign.
func Sign(key ed25519.PrivateKey, purpose string, data []byte) (Signed, error) {
	sig, err := module.Sign(key, purpose, data)
	if err != nil {
		return Signed{}, err
	}
	return Signed{Data: data, Signer: Key(key.Public().(ed25519.PublicKey)), Sig: sig}, nil
}

// DelegationPurpose is what delegations are signed for.
const DelegationPurpose = "did.delegation"

// Grant is what a delegation says: the device may act for the identity that
// signed it, for purposes within Scope, until it expires.
type Grant struct {
	// Device is the device key's did:key.
	Device  string    `json:"device"`
	Expires time.Time `json:"expires"`
	// Scope is the purposes the device may sign for: a purpose or a module
	// name, such as "chat" for "chat.event" and "chat.user"; "" or "*" for
	// any.
	Scope string `json:"scope,omitempty"`
}

// Allows reports whether the grant covers signing for purpose.
func (g Grant) Allows(purpose string) bool {
	return g.Scope == "" || g.Scope == "*" || purpose == g.Scope || strings.HasPrefix(purpose, g.Scope+".")
}

// Delegate lets the device key device act for the identity whose key is
// root, for purposes within scope ("" or "*" for any), until expires.
func Delegate(root ed25519.PrivateKey, device string, expires time.Time, scope string) (Signed, error) {
	if _, err := PublicKey(device); err != nil {
		return Signed{}, err
	}
	data, err := json.Marshal(Grant{Device: device, Expires: expires.UTC(), Scope: scope})
	if err != nil {
		return Signed{}, err
	}
	return Sign(root, DelegationPurpose, data)
}

// Grant returns what a signature's delegation grants, if it has one; it
// isn't verified.
func (s Signed) Grant() (Grant, bool) {
	if s.Delegation == nil {
		return Grant{}, false
	}
	var g Grant
	err := json.Unmarshal(s.Delegation.Data, &g)
	return g, err == nil
}

// SignAs signs data for purpose with a device's key, for the identity that
// delegated to it.
func SignAs(device ed25519.PrivateKey, delegation Signed, purpose string, data []byte) (Signed, error) {
	s, err := Sign(device, purpose, data)
	s.Delegation = &delegation
	return s, err
}

// Verify checks s's signature for purpose, and its delegation if it has one,
// as of when s was signed, as far as the verifier knows: e.g. when it first
// received s. It returns the DID of the identity s is from.
func (s Signed) Verify(purpose string, at time.Time) (string, error) {
	pub, err := PublicKey(s.Signer)
	if err != nil {
		return "", err
	}
	if !module.Verify(pub, purpose, s.Data, s.Sig) {
		return "", fmt.Errorf("bad signature by %s", s.Signer)
	}
	d := s.Delegation
	if d == nil {
		return s.Signer, nil
	}
	if d.Delegation != nil {
		return "", errors.New("devices can't delegate")
	}
	if _, err := d.Verify(DelegationPurpose, at); err != nil {
		return "", fmt.Errorf("delegation: %w", err)
	}
	var g Grant
	if err := json.Unmarshal(d.Data, &g); err != nil {
		return "", fmt.Errorf("delegation: %w", err)
	}
	if g.Device != s.Signer {
		return "", fmt.Errorf("%s delegated to %s, not %s", d.Signer, g.Device, s.Signer)
	}
	if !at.Before(g.Expires) {
		return "", fmt.Errorf("%s's delegation to %s expired at %s", d.Signer, s.Signer, g.Expires.Format(time.RFC3339))
	}
	if !g.Allows(purpose) {
		return "", fmt.Errorf("%s's delegation to %s doesn't cover %s", d.Signer, s.Signer, purpose)
	}
	return d.Signer, nil
}
