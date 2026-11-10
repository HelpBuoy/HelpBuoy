package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// SelfSigned returns a new self-signed certificate. Clients don't check it
// (see the package comment), so it carries no host names and lasts long.
func SelfSigned() (tls.Certificate, error) {
	cert, _, _, err := newSelfSigned()
	return cert, err
}

// LoadOrCreateSelfSigned loads the self-signed certificate in dir
// (tls_cert.pem and tls_key.pem), making it on first use, so it stays the
// same across restarts.
func LoadOrCreateSelfSigned(dir string) (tls.Certificate, error) {
	certFile, keyFile := filepath.Join(dir, "tls_cert.pem"), filepath.Join(dir, "tls_key.pem")
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err == nil {
		return cert, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return tls.Certificate{}, err
	}
	cert, certPEM, keyPEM, err := newSelfSigned()
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writeFile(keyFile, keyPEM); err != nil {
		return tls.Certificate{}, err
	}
	if err := writeFile(certFile, certPEM); err != nil {
		return tls.Certificate{}, err
	}
	return cert, nil
}

func newSelfSigned() (cert tls.Certificate, certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return cert, nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return cert, nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "helpbuoy-server"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(20, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return cert, nil, nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return cert, nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
	cert, err = tls.X509KeyPair(certPEM, keyPEM)
	return cert, certPEM, keyPEM, err
}

func writeFile(name string, b []byte) error {
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}
