// Package app supplies process lifecycle plumbing shared by API and Worker.
package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/operations"
	"github.com/zhigu34/one-nvr/internal/recording"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/site"
	"github.com/zhigu34/one-nvr/internal/storage"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
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
	var media *zlm.Client
	var hooksDone <-chan error
	if name == "worker" {
		apiKey, err := secret.ComponentCredential("zlm")
		if err != nil {
			return err
		}
		media, err = zlm.New("http://zlm", apiKey, nil)
		if err != nil {
			return err
		}
		hookKey, err := secret.ComponentCredential("recording-hook")
		if err != nil {
			return err
		}
		probeKey, err := secret.ComponentCredential("media-probe")
		if err != nil {
			return err
		}
		mediaProbe := probe.Runner{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe"}
		recordings := recording.New(db, media, mediaProbe, []string{"/storage"}, c.DataDir)
		baseNetwork, err := channel.ParseNetworkPolicy(c.CameraCIDRs, nil)
		if err != nil {
			return err
		}
		recordings.Sources = channel.NewSources(db, auth.NewService(db, secret, auth.NewPasswordHasher(2)), secret, baseNetwork)
		recordings.FreshNetwork = func(ctx context.Context) (channel.NetworkPolicy, error) { return freshCameraNetwork(ctx, c) }
		recordings.ProbeToken = probeKey
		pools.MediaCheck = recordings.CheckPool
		pools.MediaInspector = mediaProbe
		listener, err := net.Listen("tcp", ":8083")
		if err != nil {
			return fmt.Errorf("private media hook listener unavailable")
		}
		hookServer := &http.Server{Handler: recording.NewHookHandler(recordings, hookKey, probeKey), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8 << 10}
		completed := make(chan error, 1)
		hooksDone = completed
		go func() { completed <- hookServer.Serve(listener) }()
		defer func() {
			stop()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			hookServer.Shutdown(shutdown)
		}()
		background.Add(1)
		go func() { defer background.Done(); recordings.Replay(ctx) }()
		background.Add(1)
		go func() { defer background.Done(); recordings.RunPublisher(ctx) }()
		background.Add(1)
		go func() { defer background.Done(); recordings.RunSourceTestCleanup(ctx) }()
		background.Add(2)
		for n := 0; n < 2; n++ {
			go func() { defer background.Done(); runSourceTestJobs(ctx, recordings) }()
		}
		background.Add(4)
		for _, kind := range []string{"source.apply", "source.clear", "source.policy_apply", "source.pool_switch"} {
			go func() { defer background.Done(); runSourceChangeJobs(ctx, recordings, kind) }()
		}
		background.Add(2)
		go func() { defer background.Done(); runRecordingMonitor(ctx, recordings) }()
		go func() { defer background.Done(); recordings.MonitorIdlePools(ctx) }()
	}
	if name == "worker" {
		background.Add(1)
		go func() { defer background.Done(); pools.RunJobs(ctx) }()
	}
	if name == "worker" {
		background.Add(3)
		go func() { defer background.Done(); certificates.Monitor(ctx) }()
		go func() { defer background.Done(); certificates.RunCheckJobs(ctx) }()
		go func() { defer background.Done(); runTLSApplyJobs(ctx, certificates) }()
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
		proxyKey, err := secret.ComponentCredential("gateway")
		if err != nil {
			return err
		}
		denied := []netip.Addr{}
		public, _ := url.Parse(c.PublicURL)
		for _, host := range []string{public.Hostname(), c.MediaHost} {
			if ip, err := netip.ParseAddr(host); err == nil {
				denied = append(denied, ip)
			}
		}
		// Only resolve internal service aliases, never request-supplied camera names.
		if strings.TrimSpace(c.CameraCIDRs) != "" {
			names := []string{"api", "worker", "gateway", "postgres", "zlm"}
			if c.FrigateEnabled {
				names = append(names, "mqtt", "frigate")
			}
			if c.OpenListEnabled {
				names = append(names, "openlist")
			}
			resolveCtx, resolveCancel := context.WithTimeout(ctx, 5*time.Second)
			for _, name := range names {
				if addresses, err := net.DefaultResolver.LookupNetIP(resolveCtx, "ip", name); err == nil {
					denied = append(denied, addresses...)
				}
			}
			resolveCancel()
		}
		network, err := channel.ParseNetworkPolicy(c.CameraCIDRs, denied)
		if err != nil {
			return err
		}
		sources := channel.NewSources(db, accounts, secret, network)
		handler = httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: &site.Service{DB: db, Auth: accounts, Secrets: secret, Passwords: passwords}, Sources: sources, PublicURL: c.PublicURL, TrustedProxyToken: proxyKey, HealthCheck: check, Storage: pools, TLS: certificates, FrigateEnabled: c.FrigateEnabled, OpenListEnabled: c.OpenListEnabled})
	}
	if name == "worker" {
		prober := &operations.Prober{Checks: map[string]func(context.Context) error{"zlm": media.Health}, Operations: operations.New(db, nil, c.FrigateEnabled, c.OpenListEnabled), Client: operations.NewProbeClient(), Targets: map[string]operations.ProbeTarget{
			"gateway": {URL: "http://gateway/health", Kind: "health"},
			"api":     {URL: "http://api:8081/health/ready", Kind: "health"},
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
	case err := <-hooksDone:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("private media hook listener stopped")
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}

func freshCameraNetwork(ctx context.Context, c config.Config) (channel.NetworkPolicy, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	denied := []netip.Addr{}
	public, _ := url.Parse(c.PublicURL)
	for _, host := range []string{public.Hostname(), c.MediaHost} {
		if ip, err := netip.ParseAddr(host); err == nil {
			denied = append(denied, ip)
		}
	}
	hosts := []string{"api", "worker", "gateway", "postgres", "zlm"}
	if c.FrigateEnabled {
		hosts = append(hosts, "frigate", "mqtt")
	}
	if c.OpenListEnabled {
		hosts = append(hosts, "openlist")
	}
	for _, host := range hosts {
		ips, err := net.DefaultResolver.LookupNetIP(bounded, "ip", host)
		if err != nil {
			return channel.NetworkPolicy{}, fmt.Errorf("camera network boundary unavailable")
		}
		for _, ip := range ips {
			denied = append(denied, ip.Unmap())
		}
	}
	return channel.ParseNetworkPolicy(c.CameraCIDRs, denied)
}
