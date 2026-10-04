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
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/site"
	"log/slog"
	"net/http"
	"os/signal"
	"strings"
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
	check := func(ctx context.Context) error { return database.Ready(ctx, db) }
	var handler http.Handler = httpapi.Health(check)
	if name == "api" {
		secret, err := secrets.Load(c.DataDir)
		if err != nil {
			return fmt.Errorf("persistent secrets unavailable; run admin init-secrets before startup")
		}
		passwords := auth.NewPasswordHasher(2)
		accounts := auth.NewService(db, secret, passwords)
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
		handler = httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: &site.Service{DB: db, Secrets: secret, Passwords: passwords}, PublicURL: c.PublicURL, HealthCheck: check})
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
