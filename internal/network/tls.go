package network

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"time"

	"go-decentralized/module"
)

// certificate makes the self-signed certificate that dresses the node's
// listener in TLS, see serve. It proves nothing: nodes prove themselves
// inside sessions (noise.go), and callers don't verify it.
func certificate(key ed25519.PrivateKey) (tls.Certificate, error) {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: module.NodeID(key.Public().(ed25519.PublicKey))},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(100, 0, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
