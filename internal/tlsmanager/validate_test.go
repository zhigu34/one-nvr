package tlsmanager

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func certificateFixture(t *testing.T, kind string, serial int64, ca bool) ([]byte, []byte) {
	t.Helper()
	var key crypto.Signer
	var err error
	if strings.HasPrefix(kind, "rsa") {
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	} else {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "nvr.example.com"}, DNSNames: []string{"nvr.example.com"}, IPAddresses: []net.IP{net.ParseIP("192.0.2.1")}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: ca}
	if ca {
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	var keyDER []byte
	typ := "PRIVATE KEY"
	if kind == "rsa-pkcs1" {
		keyDER = x509.MarshalPKCS1PrivateKey(key.(*rsa.PrivateKey))
		typ = "RSA PRIVATE KEY"
	} else if kind == "ec-sec1" {
		keyDER, err = x509.MarshalECPrivateKey(key.(*ecdsa.PrivateKey))
		typ = "EC PRIVATE KEY"
	} else {
		keyDER, err = x509.MarshalPKCS8PrivateKey(key)
	}
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: keyDER})
}

func TestValidatePairSANValidityAndLimit(t *testing.T) {
	for _, kind := range []string{"rsa-pkcs1", "rsa-pkcs8", "ec-sec1", "ec-pkcs8"} {
		t.Run(kind, func(t *testing.T) {
			chain, key := certificateFixture(t, kind, 1, false)
			for _, host := range []string{"nvr.example.com", "192.0.2.1"} {
				meta, err := Validate(chain, key, host, time.Now())
				if err != nil || meta.LeafSHA256 == "" || !meta.SelfSigned {
					t.Fatalf("valid %s %+v %v", host, meta, err)
				}
			}
			_, wrongKey := certificateFixture(t, kind, 2, false)
			for _, tc := range []struct {
				chain, key []byte
				host       string
				at         time.Time
			}{{chain, wrongKey, "nvr.example.com", time.Now()}, {chain, key, "other.example.com", time.Now()}, {chain, key, "192.0.2.99", time.Now()}, {chain, key, "nvr.example.com", time.Now().Add(48 * time.Hour)}, {chain, key, "nvr.example.com", time.Now().Add(-48 * time.Hour)}, {append(chain, []byte("junk")...), key, "nvr.example.com", time.Now()}, {chain, append(key, key...), "nvr.example.com", time.Now()}, {[]byte(strings.Repeat("x", 1<<20)), key, "nvr.example.com", time.Now()}} {
				if _, err := Validate(tc.chain, tc.key, tc.host, tc.at); err == nil {
					t.Fatal("invalid pair/SAN/validity/limit accepted")
				}
			}
		})
	}
	chain, key := certificateFixture(t, "ec-pkcs8", 3, true)
	if _, err := Validate(chain, key, "nvr.example.com", time.Now()); err == nil {
		t.Fatal("CA used as server certificate")
	}
}

func signedChainFixture(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	root := &x509.Certificate{SerialNumber: big.NewInt(40), Subject: pkix.Name{CommonName: "Fixture CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, rootKey.Public(), rootKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(41), Subject: pkix.Name{CommonName: "nvr.example.com"}, DNSNames: []string{"nvr.example.com"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, leafKey.Public(), rootKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func TestValidateChainOrderAndOmittedRoot(t *testing.T) {
	leaf, root, key := signedChainFixture(t)
	for _, chain := range [][]byte{leaf, append(append([]byte(nil), leaf...), root...)} {
		if _, err := Validate(chain, key, "nvr.example.com", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	wrong, _ := certificateFixture(t, "ec-pkcs8", 99, true)
	for _, chain := range [][]byte{append(append([]byte(nil), root...), leaf...), append(append([]byte(nil), leaf...), wrong...), append(append(append([]byte(nil), leaf...), root...), root...)} {
		if _, err := Validate(chain, key, "nvr.example.com", time.Now()); err == nil {
			t.Fatal("invalid chain order/signature/duplicate accepted")
		}
	}
}

func TestValidateRejectsJunkBeforePrivateKey(t *testing.T) {
	chain, key := certificateFixture(t, "ec-pkcs8", 1, false)
	if _, err := Validate(chain, append([]byte("ignored garbage\n"), key...), "nvr.example.com", time.Now()); err == nil {
		t.Fatal("non-PEM private key prefix silently ignored")
	}
}

func TestValidateServerUsageCriticalExtensionAndCNOnly(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
	for _, kind := range []string{"client_only", "unsupported_critical", "cn_only"} {
		now := time.Now()
		cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "nvr.example.com"}, DNSNames: []string{"nvr.example.com"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		switch kind {
		case "client_only":
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		case "unsupported_critical":
			cert.ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 2, 3, 4, 5}, Critical: true, Value: []byte{5, 0}}}
		case "cn_only":
			cert.DNSNames = nil
		}
		der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
		if err != nil {
			t.Fatal(err)
		}
		chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if _, err := Validate(chain, keyPEM, "nvr.example.com", now); err == nil {
			t.Fatal("invalid server certificate accepted", kind)
		}
	}
}
