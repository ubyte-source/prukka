package control

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// The local-IPC keypair: minted at first use, self-signed, pinned by every
// client; opt-in via control.ipc_tls.
const (
	ipcCertFile = "ipc-cert.pem"
	ipcKeyFile  = "ipc-key.pem"
	// ipcServerName is the pinned identity in the minted certificate.
	ipcServerName = "prukkad"
	// ipcCertValidity is deliberately long: the keypair lives and dies with
	// the install, exactly like the control token.
	ipcCertValidity = 10 * 365 * 24 * time.Hour
)

// ServerIPCTLS returns the daemon-side TLS configuration, minting the
// keypair on first use.
func ServerIPCTLS(stateDir string) (*tls.Config, error) {
	if err := ensureIPCKeypair(stateDir); err != nil {
		return nil, err
	}

	cert, err := tls.LoadX509KeyPair(
		filepath.Join(stateDir, ipcCertFile), filepath.Join(stateDir, ipcKeyFile))
	if err != nil {
		return nil, fmt.Errorf("load ipc keypair: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		// gRPC enforces ALPN on TLS transports.
		NextProtos: []string{"h2"},
	}, nil
}

// ClientIPCTLS returns the client-side TLS configuration pinned to the
// daemon's minted certificate.
func ClientIPCTLS(stateDir string) (*tls.Config, error) {
	pemBytes, err := os.ReadFile(filepath.Clean(filepath.Join(stateDir, ipcCertFile)))
	if err != nil {
		return nil, fmt.Errorf("read pinned ipc certificate (has the daemon run once?): %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("pinned ipc certificate is not valid PEM")
	}

	return &tls.Config{
		RootCAs:    pool,
		ServerName: ipcServerName,
		MinVersion: tls.VersionTLS13,
		// gRPC enforces ALPN on TLS transports.
		NextProtos: []string{"h2"},
	}, nil
}

// ensureIPCKeypair mints the self-signed keypair unless both files exist.
func ensureIPCKeypair(stateDir string) error {
	certPath := filepath.Join(stateDir, ipcCertFile)
	keyPath := filepath.Join(stateDir, ipcKeyFile)

	if _, err := os.Stat(certPath); err == nil {
		if _, keyErr := os.Stat(keyPath); keyErr == nil {
			return nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat ipc certificate: %w", err)
	}

	certPEM, keyPEM, err := mintIPCKeypair()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write ipc key: %w", err)
	}

	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return fmt.Errorf("write ipc certificate: %w", err)
	}

	return nil
}

// mintIPCKeypair generates the self-signed certificate — its own root,
// which is all pinning needs.
func mintIPCKeypair() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ipc key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: ipcServerName},
		DNSNames:              []string{ipcServerName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(ipcCertValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("mint ipc certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("encode ipc key: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}
