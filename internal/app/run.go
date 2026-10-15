// Package app supplies process lifecycle plumbing shared by API and Worker.
package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhigu34/one-nvr/internal/archive"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/detection"
	"github.com/zhigu34/one-nvr/internal/events"
	"github.com/zhigu34/one-nvr/internal/hardware"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/live"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/notify"
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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// newDetectionService builds the one detection service both processes use.
// The publication endpoint runs on the API and the runtime probe runs on the
// worker, so an incomplete construction on either side silently breaks the
// feature — which is exactly what happened when only the worker's copy carried
// the MQTT key and reload hook. A missing field is now a startup error instead
// of a per-request "publish failed".
func newDetectionService(db *database.DB, accounts *auth.Service, dataDir, mqttKey, probeToken string) (*detection.Service, error) {
	return detection.NewComplete(db, accounts, detection.ServiceConfig{
		FrigateURL: "http://frigate:5000",
		ConfigPath: filepath.Join(dataDir, "frigate", "config.yml"),
		MQTTKey:    mqttKey,
		ProbeToken: probeToken,
		Inference:  validatedInference(dataDir),
	})
}

// validatedInference reads the deployment's hardware report and returns the
// inference profile the detector should use. Only a validated proposal is
// trusted: an unvalidated or failed probe leaves the detector on CPU, because
// pointing Frigate at a GPU the probe never confirmed makes it fail to start.
func validatedInference(dataDir string) string {
	report, err := hardware.ReadReport(dataDir)
	if err != nil || !report.Validated || report.Proposal.InferenceID == "" || report.Proposal.InferenceID == "cpu" {
		return ""
	}
	return report.Proposal.InferenceID
}

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
	poolConfig, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return fmt.Errorf("invalid database configuration")
	}
	ConfigureDatabasePool(poolConfig, name)
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
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
		viewers := &live.Service{DB: db, Auth: recordings.Sources.Auth, Media: media}
		recordings.AuthorizeLive = viewers.AuthorizePlay
		background.Add(1)
		go func() { defer background.Done(); viewers.Monitor(ctx) }()
		recordings.FreshNetwork = func(ctx context.Context) (channel.NetworkPolicy, error) { return freshCameraNetwork(ctx, c) }
		recordings.ProbeToken = probeKey
		pools.MediaCheck = recordings.CheckPool
		pools.MediaInspector = mediaProbe
		// Notification channels and delivery are the worker's, because a rule
		// hit raises its notification inside the ingestion transaction and the
		// retry loop must survive an API restart.
		notifications := notify.New(db, recordings.Sources.Auth, secret, secret.SiteID)
		background.Add(2)
		for n := 0; n < 2; n++ {
			go func() {
				defer background.Done()
				runSourceJobs(ctx, db, notify.JobKindWebhook, notifications.HandleWebhook)
			}()
		}
		// A job that exhausted its attempts is terminal in the jobs table but
		// nothing there knows it means a notification, so the delivery row is
		// swept to match.
		background.Add(1)
		go func() { defer background.Done(); runDeliveryReconciliation(ctx, notifications) }()
		// The archive service exists before any publisher starts so the
		// immediate-upload hook is set for the very first published segment.
		archiveService := archive.New(db, recordings.Sources.Auth, secret, pools)
		recordings.OnPublished = func(ctx context.Context, tx pgx.Tx, channelID, segmentID id.ID) error {
			return archiveService.EnqueueForPublication(ctx, tx, channelID, segmentID)
		}
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
		// Export tasks run at most two at a time (PRD EXP-01); a third waits in
		// the jobs queue. The reconciliation loop terminalizes tasks whose job
		// exhausted its attempts, which the jobs table cannot see.
		background.Add(2)
		for n := 0; n < 2; n++ {
			go func() { defer background.Done(); runSourceJobs(ctx, db, "recording.export", recordings.HandleExport) }()
		}
		background.Add(1)
		go func() { defer background.Done(); runExportReconciliation(ctx, recordings) }()
		background.Add(2)
		for n := 0; n < 2; n++ {
			go func() {
				defer background.Done()
				runSourceJobs(ctx, db, "recording.archive_upload", archiveService.HandleUpload)
			}()
		}
		background.Add(1)
		go func() { defer background.Done(); runArchiveScheduleSweeper(ctx, archiveService) }()
		// ARC-05 gate: the local cleanup confirms the remote copy is really
		// readable through the same client the playback proxy uses.
		recordings.ArchiveLocalGate = archiveService.ConfirmRemoteAvailable
		background.Add(1)
		go func() { defer background.Done(); runCloudExpirySweeper(ctx, archiveService) }()
		if c.FrigateEnabled {
			mqttKey, err := secret.ComponentCredential("mqtt")
			if err != nil {
				return err
			}
			// The event index lives with the worker: same database, same
			// grants, and the ingestion loop dies with the process.
			eventsService := events.New(db, recordings.Sources.Auth, "http://frigate:5000", secret.SiteID)
			eventsService.Pools = pools
			eventsService.PublicURL = c.PublicURL
			// A rule hit raises its notification inside the ingestion
			// transaction, so the worker needs the notifier that owns delivery.
			eventsService.Notify = notifications
			background.Add(2)
			go func() { defer background.Done(); eventsService.Run(ctx, "mqtt:1883", "one_nvr", mqttKey) }()
			go func() { defer background.Done(); eventsService.RunExpiry(ctx) }()
			// The detector keeps its snapshots only briefly; copying them into
			// the channel's pool is what makes the picture outlive it (STO-07).
			background.Add(1)
			go func() { defer background.Done(); runSnapshotArchive(ctx, eventsService) }()
			// Detection configuration publishes into the Frigate config bind
			// mount and makes it take effect through the detector's restart API.
			detectionService, err := newDetectionService(db, recordings.Sources.Auth, c.DataDir, mqttKey, probeKey)
			if err != nil {
				return fmt.Errorf("detection service wiring incomplete")
			}
			background.Add(2)
			go func() { defer background.Done(); detectionService.RunRuntime(ctx) }()
			// A channel gets a new media session whenever it reconnects or
			// switches source, which invalidates the input URL in the
			// published detector configuration. Reconciling keeps detection
			// running across those events instead of waiting for an operator
			// to notice and publish again.
			go func() { defer background.Done(); runDetectionSessionSync(ctx, detectionService) }()
		}
		background.Add(6)
		go func() { defer background.Done(); cleanupImportDrafts(ctx, recordings.Sources) }()
		go func() {
			defer background.Done()
			runSourceJobs(ctx, db, "source.import", recordings.ExecuteImport)
		}()
		for _, kind := range []string{"source.apply", "source.clear", "source.policy_apply", "source.pool_switch"} {
			go func() { defer background.Done(); runSourceChangeJobs(ctx, recordings, kind) }()
		}
		background.Add(3)
		go func() { defer background.Done(); runRecordingMonitor(ctx, recordings) }()
		go func() { defer background.Done(); recordings.MonitorIdlePools(ctx) }()
		// Retention runs with the recorder loops because it touches pool files
		// and publishes into the same index. It stays inert until a site sets a
		// period, so an upgraded site keeps M1's never-delete behaviour.
		go func() { defer background.Done(); recordings.RunRetention(ctx) }()
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
		apiKey, err := secret.ComponentCredential("zlm")
		if err != nil {
			return err
		}
		media, err = zlm.New("http://zlm", apiKey, nil)
		if err != nil {
			return err
		}
		var intelligence *events.Service
		var detectionService *detection.Service
		if c.FrigateEnabled {
			mqttKey, err := secret.ComponentCredential("mqtt")
			if err != nil {
				return err
			}
			intelligence = events.New(db, accounts, "http://frigate:5000", secret.SiteID)
			// The API serves the snapshot from the archived copy when one
			// exists, so it needs the pool resolver even though ingestion and
			// delivery live in the worker.
			intelligence.Pools = pools
			intelligence.PublicURL = c.PublicURL
			// The publication endpoint is served by the API process, so the
			// API's detection service needs the same render input and reload
			// path as the worker's — sharing one constructor is what keeps
			// the two from drifting again.
			apiProbeKey, err := secret.ComponentCredential("media-probe")
			if err != nil {
				return err
			}
			detectionService, err = newDetectionService(db, accounts, c.DataDir, mqttKey, apiProbeKey)
			if err != nil {
				return fmt.Errorf("detection service wiring incomplete")
			}
		}
		handler = httpapi.NewHandler(httpapi.Dependencies{Events: intelligence, Detection: detectionService, Auth: accounts, Site: &site.Service{DB: db, Auth: accounts, Secrets: secret, Passwords: passwords}, Sources: sources, Live: &live.Service{DB: db, Auth: accounts, Media: media}, PublicURL: c.PublicURL, TrustedProxyToken: proxyKey, HealthCheck: check, Storage: pools, TLS: certificates, FrigateEnabled: c.FrigateEnabled, OpenListEnabled: c.OpenListEnabled, Archive: archive.New(db, accounts, secret, pools), Notify: notify.New(db, accounts, secret, secret.SiteID)})
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
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return channel.NetworkPolicy{}, fmt.Errorf("camera network boundary unavailable")
	}
	var connected []netip.Prefix
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil && !prefix.Addr().IsLoopback() {
			connected = append(connected, prefix.Masked())
		}
	}
	return freshCameraNetworkResolved(ctx, c, net.DefaultResolver.LookupNetIP, connected)
}
func freshCameraNetworkResolved(ctx context.Context, c config.Config, lookup func(context.Context, string, string) ([]netip.Addr, error), connected []netip.Prefix) (channel.NetworkPolicy, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var denied []netip.Addr
	public, _ := url.Parse(c.PublicURL)
	for _, host := range []string{public.Hostname(), c.MediaHost} {
		if ip, err := netip.ParseAddr(host); err == nil {
			denied = append(denied, ip)
		}
	}
	for _, host := range []string{"api", "worker", "gateway", "postgres", "zlm"} {
		ips, err := lookup(bounded, "ip", host)
		if err != nil {
			return channel.NetworkPolicy{}, fmt.Errorf("camera network boundary unavailable")
		}
		for _, ip := range ips {
			denied = append(denied, ip.Unmap())
		}
	}
	var optional []string
	if c.FrigateEnabled {
		optional = append(optional, "frigate", "mqtt")
	}
	if c.OpenListEnabled {
		optional = append(optional, "openlist")
	}
	for _, host := range optional {
		ips, err := lookup(bounded, "ip", host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			denied = append(denied, ip.Unmap())
		}
	}
	policy, err := channel.ParseNetworkPolicy(c.CameraCIDRs, denied)
	// Connected container networks remain denied even when an optional endpoint
	// has no DNS record. Explicit camera /32 or /128 fixture mappings may overlap,
	// but resolved management IPs always take precedence.
	policy.DeniedNetworks = append([]netip.Prefix(nil), connected...)
	return policy, err
}

// Media operations own pinned connections while performing fenced external work.
// pgxpool's CPU-derived default can be four on N5105/CI, leaving no connection for
// their subsequent queries, lease renewals or TLS jobs. Keep eight spare above
// the fifteen bounded Worker owners; connections are opened lazily.
func ConfigureDatabasePool(c *pgxpool.Config, name string) {
	c.MaxConns = 8
	if name == "worker" {
		c.MaxConns = 24
	}
}
