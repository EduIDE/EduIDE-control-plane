// Command certgen writes a test CA and a server certificate for the e2e hosts.
// It exists because the openssl of macOS (LibreSSL) differs from the one of Linux CI.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	out := flag.String("out", ".e2e/certs", "output directory")
	hosts := flag.String("hosts", "", "comma separated DNS names of the server certificate")
	flag.Parse()
	if *hosts == "" {
		log.Fatal("-hosts is required")
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		log.Fatal(err)
	}
	caKey := newKey()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "eduide-e2e-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(30 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER := create(caTmpl, caTmpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)

	key := newKey()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "eduide-e2e"},
		DNSNames:     strings.Split(*hosts, ","),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der := create(tmpl, caCert, &key.PublicKey, caKey)

	write(*out, "ca.crt", "CERTIFICATE", caDER, 0o644)
	write(*out, "tls.crt", "CERTIFICATE", der, 0o644)
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		log.Fatal(err)
	}
	write(*out, "tls.key", "EC PRIVATE KEY", keyDER, 0o600)
}

func newKey() *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	return k
}

func create(tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) []byte {
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		log.Fatal(err)
	}
	return der
}

func write(dir, name, typ string, der []byte, mode os.FileMode) {
	b := pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, name), b, mode); err != nil {
		log.Fatal(err)
	}
}
