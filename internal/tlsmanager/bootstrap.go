package tlsmanager

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"math/big"
	"net"
	"time"
)

// Bootstrap is a local deployment operation, never a public API. A manual HTTPS
// installation gets one private self-signed version before account setup.
// Existing certificate state is authoritative and is never replaced on restart.
func (s *Service) Bootstrap(ctx context.Context) error {
	if s.Protocol != "https" || s.InputDir != "" {
		return nil
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if e := lockTLS(ctx, tx); e != nil {
			return e
		}
		state, e := s.currentTx(ctx, tx)
		if e != nil {
			return e
		}
		if state.ActiveID != nil || state.CandidateID != nil || state.DesiredID != nil {
			return nil
		}
		var count int
		if e = tx.QueryRow(ctx, "SELECT count(*) FROM tls_certificates").Scan(&count); e != nil {
			return e
		}
		if count > 0 {
			return nil
		}
		chain, key, e := selfSigned(s.Host)
		if e != nil {
			return e
		}
		v, e := s.importTx(ctx, tx, chain, key, "manual")
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE gateway_tls_state SET candidate_id=$1 WHERE singleton", v.ID); e != nil {
			return e
		}
		if _, e = s.queueApplyTx(ctx, tx, state, v.ID, "apply", ""); e != nil {
			return e
		}
		return audit.Append(ctx, tx, audit.Entry{Action: "tls.bootstrap_queued", ObjectID: v.ID})
	})
}
func selfSigned(host string) ([]byte, []byte, error) {
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return nil, nil, e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return nil, nil, e
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	if ip := net.ParseIP(host); ip != nil {
		cert.IPAddresses = []net.IP{ip}
	} else {
		cert.DNSNames = []string{host}
	}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if e != nil {
		return nil, nil, e
	}
	priv, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		return nil, nil, e
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), nil
}
