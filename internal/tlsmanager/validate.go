package tlsmanager

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"github.com/zhigu34/one-nvr/internal/fault"
	"time"
)

const MaxPEMBytes = 1 << 20

type Metadata struct {
	Subject      string    `json:"subject"`
	CommonName   string    `json:"common_name"`
	Issuer       string    `json:"issuer"`
	SANs         []string  `json:"sans"`
	SerialNumber string    `json:"serial_number"`
	NotBefore    time.Time `json:"not_before"`
	NotAfter     time.Time `json:"not_after"`
	LeafSHA256   string    `json:"leaf_sha256"`
	Algorithm    string    `json:"algorithm"`
	KeyBits      int       `json:"key_bits"`
	SelfSigned   bool      `json:"self_signed"`
	ChainStatus  string    `json:"chain_status"`
}

func validationError(code string) error {
	return fault.New(422, code, "证书校验失败，请检查证书、密钥、入口主机和有效期")
}

func Validate(chain, key []byte, host string, now time.Time) (Metadata, error) {
	var out Metadata
	if len(chain) == 0 || len(key) == 0 || len(chain)+len(key) > MaxPEMBytes {
		return out, validationError("tls_size_invalid")
	}
	var certs []*x509.Certificate
	remaining := bytes.TrimSpace(chain)
	for len(remaining) > 0 {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return out, validationError("tls_chain_invalid")
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(certs) >= 16 {
			return out, validationError("tls_chain_invalid")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return out, validationError("tls_chain_invalid")
		}
		for _, prior := range certs {
			if bytes.Equal(prior.Raw, cert.Raw) {
				return out, validationError("tls_chain_invalid")
			}
		}
		if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
			return out, validationError("tls_validity_invalid")
		}
		certs = append(certs, cert)
		remaining = bytes.TrimSpace(rest)
	}
	if len(certs) == 0 {
		return out, validationError("tls_chain_invalid")
	}
	keyInput := bytes.TrimSpace(key)
	if !bytes.HasPrefix(keyInput, []byte("-----BEGIN ")) {
		return Metadata{}, validationError("tls_key_invalid")
	}
	keyBlock, keyRest := pem.Decode(keyInput)
	if keyBlock == nil || len(bytes.TrimSpace(keyRest)) != 0 || len(keyBlock.Headers) != 0 || (keyBlock.Type != "PRIVATE KEY" && keyBlock.Type != "RSA PRIVATE KEY" && keyBlock.Type != "EC PRIVATE KEY") {
		return out, validationError("tls_key_invalid")
	}
	pair, err := tls.X509KeyPair(chain, key)
	if err != nil {
		return out, validationError("tls_key_mismatch")
	}
	switch private := pair.PrivateKey.(type) {
	case *rsa.PrivateKey:
		out.Algorithm = "RSA"
		out.KeyBits = private.N.BitLen()
		if out.KeyBits < 2048 {
			return Metadata{}, validationError("tls_key_unsupported")
		}
	case *ecdsa.PrivateKey:
		out.Algorithm = "EC"
		out.KeyBits = private.Curve.Params().BitSize
		if out.KeyBits < 256 {
			return Metadata{}, validationError("tls_key_unsupported")
		}
	default:
		return out, validationError("tls_key_unsupported")
	}
	leaf := certs[0]
	if leaf.IsCA || len(leaf.UnhandledCriticalExtensions) > 0 || (leaf.KeyUsage != 0 && leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0) {
		return Metadata{}, validationError("tls_usage_invalid")
	}
	if len(leaf.ExtKeyUsage) > 0 || len(leaf.UnknownExtKeyUsage) > 0 {
		valid := false
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
				valid = true
			}
		}
		if !valid {
			return Metadata{}, validationError("tls_usage_invalid")
		}
	}
	if host == "" || leaf.VerifyHostname(host) != nil {
		return Metadata{}, validationError("tls_host_mismatch")
	}
	for i := 0; i < len(certs)-1; i++ {
		if !bytes.Equal(certs[i].RawIssuer, certs[i+1].RawSubject) || certs[i].CheckSignatureFrom(certs[i+1]) != nil {
			return Metadata{}, validationError("tls_chain_invalid")
		}
	}
	if len(certs) > 1 {
		roots := x509.NewCertPool()
		roots.AddCert(certs[len(certs)-1])
		intermediates := x509.NewCertPool()
		for _, parent := range certs[1 : len(certs)-1] {
			intermediates.AddCert(parent)
		}
		// The supplied last issuer is only a validation anchor for this provided
		// chain; this does not assert that a browser trusts that issuer.
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: host, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return Metadata{}, validationError("tls_chain_invalid")
		}
	}
	out.Subject = leaf.Subject.String()
	out.CommonName = leaf.Subject.CommonName
	out.Issuer = leaf.Issuer.String()
	out.SerialNumber = leaf.SerialNumber.Text(16)
	out.SANs = append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		out.SANs = append(out.SANs, ip.String())
	}
	out.NotBefore = leaf.NotBefore.UTC()
	out.NotAfter = leaf.NotAfter.UTC()
	fingerprint := sha256.Sum256(leaf.Raw)
	out.LeafSHA256 = hex.EncodeToString(fingerprint[:])
	out.SelfSigned = bytes.Equal(leaf.RawSubject, leaf.RawIssuer) && leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
	out.ChainStatus = "issuer_not_provided"
	if len(certs) > 1 {
		out.ChainStatus = "provided_chain_verified"
	} else if out.SelfSigned {
		out.ChainStatus = "self_signed"
	}
	// No public or enterprise trust-store claim is inferred from pair validation.
	return out, nil
}
