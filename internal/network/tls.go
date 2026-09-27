package network

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"time"

	"go-decentralized/module"
)

// Nodes authenticate each other with TLS, trusting keys rather than
// certificate authorities: a node's certificate is self-signed with its key,
// its ID is the hash of that key, and the handshake proves the peer holds it.

// certificate makes the node's self-signed TLS certificate.
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

// tlsConfig configures TLS with a peer, on either side of a connection. If
// expect isn't empty, the peer must prove to be that node.
func (n *Network) tlsConfig(expect string) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{n.cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
		ClientAuth:   tls.RequireAnyClientCert,
		// Peers are verified by key below, not by certificate authorities.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no certificate")
			}
			cert, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			id := certNode(cert)
			if id == "" {
				return errors.New("certificate has no ed25519 key")
			}
			if expect != "" && id != expect {
				return fmt.Errorf("reached node %.8s, not %.8s", id, expect)
			}
			return nil
		},
	}
}

// certNode returns the ID of the node a certificate belongs to.
func certNode(cert *x509.Certificate) string {
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return ""
	}
	return module.NodeID(pub)
}

// peerNode returns the ID of the node at the other end of a TLS connection.
func peerNode(state tls.ConnectionState) string {
	if len(state.PeerCertificates) == 0 {
		return ""
	}
	return certNode(state.PeerCertificates[0])
}
