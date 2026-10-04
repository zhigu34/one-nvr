package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"net"
	"net/http"
	"net/url"
	"time"
)

func verifyCommand(args []string) (bool, error) {
	if len(args) != 1 || (args[0] != "verify-entry" && args[0] != "verify-optional") {
		return false, nil
	}
	c, e := config.Load()
	if e != nil {
		return true, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	if args[0] == "verify-optional" {
		client := &http.Client{Timeout: 3 * time.Second}
		targets := []string{}
		if c.FrigateEnabled {
			targets = append(targets, "http://frigate:5000/api/version")
		}
		if c.OpenListEnabled {
			targets = append(targets, "http://openlist:5244/api/public/settings")
		}
		for _, target := range targets {
			if e = waitHTTP(ctx, client, target); e != nil {
				return true, fmt.Errorf("enabled optional service unavailable")
			}
		}
		return true, nil
	}
	u, _ := url.Parse(c.PublicURL)
	target := "gateway:80"
	if u.Scheme == "https" {
		target = "gateway:443"
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, target)
	}, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, ServerName: u.Hostname()}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	db, e := database.Open(ctx, c.DatabaseURL)
	if e != nil {
		return true, fmt.Errorf("database unavailable")
	}
	defer db.Pool.Close()
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, "GET", c.PublicURL+"/api/v1/setup/status", nil)
		r, err := client.Do(req)
		ok := err == nil && r.StatusCode == 200
		if ok && u.Scheme == "https" {
			var fingerprint string
			err = db.Pool.QueryRow(ctx, "SELECT metadata->>'leaf_sha256' FROM tls_certificates WHERE id=(SELECT active_id FROM gateway_tls_state WHERE singleton)").Scan(&fingerprint)
			ok = err == nil && r.TLS != nil && len(r.TLS.PeerCertificates) > 0
			if ok {
				leaf := r.TLS.PeerCertificates[0]
				sum := sha256.Sum256(leaf.Raw)
				ok = hex.EncodeToString(sum[:]) == fingerprint && leaf.VerifyHostname(u.Hostname()) == nil
			}
		}
		if r != nil {
			r.Body.Close()
		}
		if ok {
			keyState, e := secrets.Load(c.DataDir)
			if e != nil {
				return true, e
			}
			key, _ := keyState.ComponentCredential("zlm")
			if e = verifyZLM(ctx, key); e != nil {
				return true, e
			}
			return true, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return true, fmt.Errorf("entry not ready or certificate fingerprint not verified")
}
func waitHTTP(ctx context.Context, c *http.Client, target string) error {
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
		r, e := c.Do(req)
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return ctx.Err()
}
func verifyZLM(ctx context.Context, key string) error {
	c := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://zlm/index/api/getThreadsLoad?secret="+key, nil)
	r, e := c.Do(req)
	if e != nil {
		return fmt.Errorf("ZLM private API unavailable")
	}
	defer r.Body.Close()
	var v struct {
		Code *int `json:"code"`
	}
	if json.NewDecoder(r.Body).Decode(&v) != nil || r.StatusCode != 200 || v.Code == nil || *v.Code != 0 {
		return fmt.Errorf("ZLM private API refused readiness")
	}
	return nil
}
