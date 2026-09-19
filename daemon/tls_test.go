package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A self-signed EC P-256 pair, which is what OpenWrt's px5g generates for
// uhttpd, written out in both encodings.
func writePair(t *testing.T, asPEM bool) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "OpenWrt"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certOut, keyOut := certDER, keyDER
	if asPEM {
		certOut = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
		keyOut = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	}
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "c"), filepath.Join(dir, "k")
	if err := os.WriteFile(certPath, certOut, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyOut, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestLoadKeyPair(t *testing.T) {
	for _, asPEM := range []bool{false, true} {
		c, k := writePair(t, asPEM)
		pair, err := loadKeyPair(c, k)
		if err != nil {
			t.Fatalf("pem=%v: %v", asPEM, err)
		}
		if len(pair.Certificate) != 1 || pair.PrivateKey == nil {
			t.Fatalf("pem=%v: incomplete pair", asPEM)
		}
	}
	c, _ := writePair(t, false)
	if _, err := loadKeyPair(c, c); err == nil {
		t.Fatal("a certificate passed as its own key must be refused")
	}
}
