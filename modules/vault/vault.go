// Package vault keeps people's root identities for those without a vault of
// their own. A person's browser derives two secrets from their passkey (or a
// passphrase): a key that wraps the root, which never leaves the browser,
// and a proof, whose hash names the account. The vault keeps the wrapped
// root and hands it to whoever presents the proof. It can't read what it
// keeps, and signs nothing. See docs/design/identity-auth.md.
package vault

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"time"

	"go-decentralized/did"
	"go-decentralized/module"
)

const Name = "vault"

// SavePurpose is what a root signs to keep itself in the vault.
const SavePurpose = "vault.save"

//go:embed module.yaml
var manifest []byte

const (
	minProof = 16   // bytes
	maxBlob  = 8192 // bytes, encoded
)

type Module struct {
	env module.Env
}

func New(_ func(any) error, env module.Env) (module.Module, error) {
	return &Module{env: env}, nil
}

func (m *Module) Manifest() module.Manifest { return module.MustParseManifest(manifest) }

func (m *Module) Handlers() map[string]module.Handler {
	return map[string]module.Handler{
		"save": module.HandlerFor(m.save),
		"open": module.HandlerFor(m.open),
	}
}

// Account is what the vault keeps for a person: their wrapped root, and whose
// it is, so only that root can replace it.
type Account struct {
	Root    string          `json:"root"`
	Blob    json.RawMessage `json:"blob"`
	Created time.Time       `json:"created"`
	Updated time.Time       `json:"updated"`
}

// accountID names the account a proof opens: the proof's hash, so the vault
// keeps no proofs.
func accountID(proof []byte) (string, error) {
	if len(proof) < minProof {
		return "", module.Errorf(module.CodeInvalidArgument, "a proof is at least %d bytes", minProof)
	}
	sum := sha256.Sum256(proof)
	return hex.EncodeToString(sum[:]), nil
}

func (m *Module) save(ctx context.Context, in struct {
	Signed did.Signed `json:"signed"`
}) (map[string]any, error) {
	// The root itself signs, not a device: a device authorized by the root
	// mustn't be able to lock the person out by replacing the blob.
	if in.Signed.Delegation != nil {
		return nil, module.Errorf(module.CodeInvalidArgument, "the root itself signs what the vault keeps")
	}
	root, err := in.Signed.Verify(SavePurpose, time.Now())
	if err != nil {
		return nil, module.Errorf(module.CodeInvalidArgument, "%v", err)
	}
	var req struct {
		Proof []byte          `json:"proof"`
		Blob  json.RawMessage `json:"blob"`
	}
	if err := json.Unmarshal(in.Signed.Data, &req); err != nil {
		return nil, module.Errorf(module.CodeInvalidArgument, "signed data: %v", err)
	}
	id, err := accountID(req.Proof)
	if err != nil {
		return nil, err
	}
	if len(req.Blob) > maxBlob {
		return nil, module.Errorf(module.CodeInvalidArgument, "a blob is at most %d bytes", maxBlob)
	}
	var a Account
	version, err := m.env.Entities("account").Get(ctx, id, &a)
	now := time.Now().UTC()
	switch module.Code(err) {
	case "":
		if a.Root != root {
			return nil, module.Errorf(module.CodePermissionDenied, "the account is another root's")
		}
		a.Updated = now
	case module.CodeNotFound:
		a = Account{Root: root, Created: now, Updated: now}
	default:
		return nil, err
	}
	a.Blob = req.Blob
	version, err = m.env.Entities("account").PutIf(ctx, id, a, version)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "root": root, "version": version}, nil
}

func (m *Module) open(ctx context.Context, in struct {
	Proof []byte `json:"proof"`
}) (map[string]any, error) {
	id, err := accountID(in.Proof)
	if err != nil {
		return nil, err
	}
	var a Account
	version, err := m.env.Entities("account").Get(ctx, id, &a)
	if module.Code(err) == module.CodeNotFound {
		return nil, module.Errorf(module.CodeNotFound, "no such account")
	} else if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "root": a.Root, "blob": a.Blob, "version": version}, nil
}
