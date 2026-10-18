package host

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Fingerprint returns the lowercase hex SHA-256 of the leaf certificate in a PEM file.
func Fingerprint(certFile string) (string, error) {
	b, err := os.ReadFile(certFile)
	if err != nil {
		return "", err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return "", fmt.Errorf("no PEM block in %s", certFile)
	}
	sum := sha256.Sum256(blk.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// EnsureSelfSigned creates (or reuses, if still valid >30 days) an ECDSA P-256
// self-signed certificate under dir. Returns cert/key paths.
func EnsureSelfSigned(dir string, extraHosts []string) (certFile, keyFile string, err error) {
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if b, e := os.ReadFile(certFile); e == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			if c, e := x509.ParseCertificate(blk.Bytes); e == nil && time.Until(c.NotAfter) > 30*24*time.Hour {
				if _, e := os.Stat(keyFile); e == nil {
					return
				}
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "redxd self-signed", Organization: []string{"RedX"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(825 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	hosts := append([]string{"localhost", "127.0.0.1", "::1"}, extraHosts...)
	if hn, e := os.Hostname(); e == nil {
		hosts = append(hosts, hn)
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	if err = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return
	}
	err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return
}

// TLSConfig builds the server TLS config for cfg (nil when TLS is off).
func (h *Host) TLSConfig() (*tls.Config, string, error) {
	var cert, key string
	switch h.Cfg.TLS {
	case "auto":
		var err error
		cert, key, err = EnsureSelfSigned(filepath.Join(h.Cfg.DataDir, "tls"), h.Cfg.TLSHosts)
		if err != nil {
			return nil, "", err
		}
	case "files":
		cert, key = h.Cfg.TLSCert, h.Cfg.TLSKey
	default:
		return nil, "", nil
	}
	kp, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, "", err
	}
	fp, _ := Fingerprint(cert)
	return &tls.Config{Certificates: []tls.Certificate{kp}, MinVersion: tls.VersionTLS12}, fp, nil
}
