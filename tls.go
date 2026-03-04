package testworld

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// Well-known CA bundle paths on the host, used to build a combined trust
// store that includes the testworld CA without per-container Docker API calls.
var caBundlePaths = []string{
	"/etc/ssl/certs/ca-certificates.crt", // Debian/Ubuntu/Alpine
	"/etc/pki/tls/certs/ca-bundle.crt",   // Red Hat/CentOS
}

const (
	// TLSCACertPath is the in-container path to the world CA certificate.
	TLSCACertPath = "/tls/ca.crt"
	// TLSCertPath is the in-container path to the container's leaf certificate.
	TLSCertPath = "/tls/cert.pem"
	// TLSKeyPath is the in-container path to the container's private key.
	TLSKeyPath = "/tls/key.pem"
)

// worldCA holds a per-World ephemeral certificate authority used to issue
// TLS certificates for containers.
type worldCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	// bundlePEM is the host's CA bundle with the testworld CA appended.
	// Mounted directly into containers to avoid per-container Docker API
	// calls that would otherwise read-modify-write the trust store.
	bundlePEM []byte
}

// newWorldCA generates a self-signed ECDSA P-256 CA certificate valid for one hour.
func newWorldCA() (*worldCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate CA serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "testworld-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	// Build a combined CA bundle by reading the host's trust store and
	// appending our CA. This is mounted directly into containers, avoiding
	// per-container Docker API calls to read-modify-write the trust store.
	var bundlePEM []byte
	for _, p := range caBundlePaths {
		if data, err := os.ReadFile(p); err == nil {
			bundlePEM = data
			break
		}
	}
	if len(bundlePEM) > 0 && bundlePEM[len(bundlePEM)-1] != '\n' {
		bundlePEM = append(bundlePEM, '\n')
	}
	bundlePEM = append(bundlePEM, certPEM...)

	return &worldCA{cert: cert, key: key, certPEM: certPEM, bundlePEM: bundlePEM}, nil
}

// generateCert creates a leaf certificate signed by the CA. The certificate
// includes the given DNS names plus "localhost", and IP SANs for 127.0.0.1
// and ::1. It is valid for both server and client authentication.
func (ca *worldCA) generateCert(names []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate leaf key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate leaf serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     append(names, "localhost"),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("create leaf certificate: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal leaf key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}
