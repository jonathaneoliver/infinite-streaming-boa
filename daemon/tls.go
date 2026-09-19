package main

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// loadKeyPair reads a certificate and its private key in either PEM or DER.
//
// DER because that is what OpenWrt has: uhttpd's init script generates
// /etc/uhttpd.crt and /etc/uhttpd.key with `px5g selfsigned -der`, and serving
// boa over https with the certificate LuCI already uses means the browser has
// one self-signed certificate to accept for the box, not two. tls.LoadX509KeyPair
// accepts PEM only, so the DER case is assembled by hand.
func loadKeyPair(certPath, keyPath string) (tls.Certificate, error) {
	certRaw, err := os.ReadFile(certPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyRaw, err := os.ReadFile(keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if b, _ := pem.Decode(certRaw); b != nil {
		return tls.X509KeyPair(certRaw, keyRaw)
	}
	if _, err := x509.ParseCertificate(certRaw); err != nil {
		return tls.Certificate{}, fmt.Errorf("%s is neither PEM nor a DER certificate: %w", certPath, err)
	}
	key, err := parseDERKey(keyRaw)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("%s: %w", keyPath, err)
	}
	return tls.Certificate{Certificate: [][]byte{certRaw}, PrivateKey: key}, nil
}

// parseDERKey accepts the three DER encodings a private key arrives in. px5g
// writes EC keys as SEC1 and RSA keys as PKCS#1; PKCS#8 covers anything else.
func parseDERKey(der []byte) (crypto.PrivateKey, error) {
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("not a DER EC, PKCS#1 or PKCS#8 private key")
}
