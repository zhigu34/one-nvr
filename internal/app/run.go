// Package app supplies process lifecycle plumbing shared by API and Worker.
package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"github.com/zhigu34/one-nvr/internal/operations"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/site"
	"github.com/zhigu34/one-nvr/internal/storage"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"log/slog"
	"net/http"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func Run(name string) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("ONE_NVR_DATABASE_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, c.DatabaseURL)
	if err != nil {
		return fmt.Errorf("invalid database configuration")
	}
	defer pool.Close()
	db := &database.DB{Pool: pool}
	secret, err := secrets.Load(c.DataDir)
	if err != nil {
		return fmt.Errorf("persistent secrets unavailable; run admin init-secrets before startup")
	}
	check := func(ctx context.Context) error { return database.Ready(ctx, db) }
	var handler http.Handler = httpapi.Health(check)
	pools := storage.New(db, nil, []string{"/storage"})
	tlsInput := ""
	if c.TLSDir != "" {
		tlsInput = "/tls-input"
	}
	certificates := tlsmanager.New(db, nil, c.DataDir, c.PublicURL, tlsInput)
	initializeTLS, cancelTLS := context.WithTimeout(ctx, 10*time.Second)
	err = certificates.Initialize(initializeTLS)
	cancelTLS()
	if err != nil {
		return fmt.Errorf("TLS state initialization unavailable")
	}
	var background sync.WaitGroup
	defer func() { stop(); background.Wait() }()
	background.Add(1)
	go func() { defer background.Done(); pools.Monitor(ctx, name) }()
	if name == "worker" {
		background.Add(1)
		go func() { defer background.Done(); pools.RunJobs(ctx) }()
	}
	if name == "worker" {
		background.Add(2)
		go func() { defer background.Done(); certificates.Monitor(ctx) }()
		go func() { defer background.Done(); certificates.RunCheckJobs(ctx) }()
	}
	if name == "api" {
		passwords := auth.NewPasswordHasher(2)
		accounts := auth.NewService(db, secret, passwords)
		pools.Auth = accounts
		certificates.Auth = accounts
		scheme := "http"
		if strings.HasPrefix(c.PublicURL, "https://") {
			scheme = "https"
		}
		startup, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = accounts.ApplyEntryProtocol(startup, scheme)
		cancel()
		if err != nil {
			return fmt.Errorf("entry protocol initialization unavailable")
		}
		handler = httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: &site.Service{DB: db, Auth: accounts, Secrets: secret, Passwords: passwords}, PublicURL: c.PublicURL, HealthCheck: check, Storage: pools, TLS: certificates, FrigateEnabled: c.FrigateEnabled, OpenListEnabled: c.OpenListEnabled})
	}
	if name == "worker" {
		zlmKey, err := secret.ComponentCredential("zlm")
		if err != nil {
			return err
		}
		prober := &operations.Prober{Operations: operations.New(db, nil, c.FrigateEnabled, c.OpenListEnabled), Client: operations.NewProbeClient(), Targets: map[string]operations.ProbeTarget{
			"gateway": {URL: "http://gateway/health", Kind: "health"},
			"api":     {URL: "http://api:8081/health/ready", Kind: "health"},
			"zlm":     {URL: "http://zlm/index/api/getThreadsLoad?secret=" + zlmKey, Kind: "zlm"},
		}}
		if c.FrigateEnabled {
			mqttKey, err := secret.ComponentCredential("mqtt")
			if err != nil {
				return err
			}
			prober.MQTTAddress = "mqtt:1883"
			prober.MQTTUsername = "one_nvr"
			prober.MQTTPassword = mqttKey
			prober.Targets["frigate"] = operations.ProbeTarget{URL: "http://frigate:5000/api/version", Kind: "version"}
		}
		if c.OpenListEnabled {
			prober.Targets["openlist"] = operations.ProbeTarget{URL: "http://openlist:5244/api/public/settings", Kind: "openlist"}
		}
		probeDone := make(chan struct{})
		go func() { defer close(probeDone); prober.Run(ctx) }()
		defer func() { stop(); <-probeDone }()
	}
	server := &http.Server{Addr: c.ListenAddress, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	slog.Info("foundation process started", "service", name, "listen", c.ListenAddress)
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}
