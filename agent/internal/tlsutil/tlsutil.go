// Package tlsutil manages the agent's self-signed certificate and the
// fingerprint-pinning client configuration.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/tobiaswadsethdev/hedera/agent/internal/config"
)

// LoadOrCreate loads dir/cert.pem and dir/key.pem, generating an ECDSA P-256
// self-signed pair valid for ten years if they don't exist.
func LoadOrCreate(dir, commonName string) (tls.Certificate, error) {
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err == nil {
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
		return cert, err
	}
	if _, statErr := os.Stat(certPath); !errors.Is(statErr, os.ErrNotExist) {
		return tls.Certificate{}, fmt.Errorf("loading TLS key pair: %w", err)
	}
	if err := config.EnsureDir(dir); err != nil {
		return tls.Certificate{}, err
	}
	certPEM, keyPEM, err := generate(commonName)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := config.WriteFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := config.WriteFileAtomic(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	cert, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	return cert, err
}

func generate(commonName string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// Fingerprint is "sha256:" + base64url(SHA-256(SubjectPublicKeyInfo)).
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// ServerConfig returns a TLS 1.3 server configuration for cert.
func ServerConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
}

// ErrPinMismatch means the server's key is not the pinned one.
var ErrPinMismatch = errors.New("tls: server key does not match the pinned fingerprint")

// PinnedClientConfig accepts exactly the server whose leaf certificate has
// fingerprint fp, ignoring the system trust store and hostnames.
func PinnedClientConfig(fp string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Chain and hostname verification are replaced by the pin check below.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return ErrPinMismatch
			}
			got := Fingerprint(cs.PeerCertificates[0])
			if subtle.ConstantTimeCompare([]byte(got), []byte(fp)) != 1 {
				return ErrPinMismatch
			}
			return nil
		},
	}
}
