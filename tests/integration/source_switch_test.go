package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/recording"
)

func TestSourceTestRequestDoesNotInterruptOldRun(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2 WHERE id=$1", f.Channel, f.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_runs SET state='recording' WHERE id=$1", f.Run); err != nil {
		t.Fatal(err)
	}
	network, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	sources := channel.NewSources(f.DB, f.Auth, f.Site.Secrets, network)
	draft, err := sources.CreateDraft(ctx, f.Admin, f.Channel, 2, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.21", MainPath: "/main", SubPath: "/sub"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
	if err != nil {
		t.Fatal(err)
	}
	change, err := sources.RequestTest(ctx, f.Admin, f.Channel, draft.ID, "test-new-source")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := sources.RequestTest(ctx, f.Admin, f.Channel, draft.ID, "test-new-source")
	if err != nil || replay.JobID != change.JobID {
		t.Fatal("test request not idempotent", err)
	}
	if _, err := sources.RequestTest(ctx, f.Admin, f.Channel, f.Revision, "test-new-source"); !errors.Is(err, jobs.ErrIdempotencyConflict) {
		t.Fatal("changed request reused key", err)
	}
	var testID, current id.ID
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM source_tests WHERE job_id=$1", change.JobID).Scan(&testID); err != nil {
		t.Fatal(err)
	}
	result, err := sources.TestResult(ctx, f.Admin, f.Channel, testID)
	if err != nil || result.State != "queued" || result.RevisionID != draft.ID {
		t.Fatal("queued source result missing", result, err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", f.Channel).Scan(&current); err != nil || current != f.Revision {
		t.Fatal("test request changed current source", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&state); err != nil || state != "recording" {
		t.Fatal("test request interrupted old recorder", err)
	}
	raw, _ := json.Marshal(result)
	if string(raw) == "" {
		t.Fatal("empty result")
	}
}
func TestSourceTestClaimsGlobalTwoSlotsAcrossWorkers(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	network, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	sources := channel.NewSources(db, accounts, sites.Secrets, network)
	for n := 1; n <= 3; n++ {
		var ch id.ID
		if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=$1", n).Scan(&ch); err != nil {
			t.Fatal(err)
		}
		revision, err := sources.CreateDraft(ctx, admin.Principal, ch, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sources.RequestTest(ctx, admin.Principal, ch, revision.ID, string(ch)); err != nil {
			t.Fatal(err)
		}
	}
	repo := jobs.Repository{DB: db}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var claims []jobs.Lease
	for n := 0; n < 3; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := repo.Claim(ctx, "source.test")
			if errors.Is(err, jobs.ErrNoJob) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			claims = append(claims, lease)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(claims) != 2 {
		t.Fatalf("global test concurrency=%d, want 2", len(claims))
	}
	var queued int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='source.test' AND state='queued' AND attempt=0").Scan(&queued); err != nil || queued != 1 {
		t.Fatal("waiting source test consumed an attempt", queued, err)
	}
	if err := repo.Complete(ctx, claims[0], jobs.Result{Payload: []byte(`{"state":"succeeded"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Claim(ctx, "source.test"); err != nil {
		t.Fatal("completed slot not reusable", err)
	}
	// An expired final-attempt ordinary source test stays bounded, unlike apply.
	if _, err := db.Pool.Exec(ctx, "UPDATE jobs SET attempt=max_attempts,lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", claims[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Claim(ctx, "source.test"); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatal("exhausted source test reclaimed", err)
	}
	var state string
	if err := db.Pool.QueryRow(ctx, "SELECT state FROM jobs WHERE id=$1", claims[1].ID).Scan(&state); err != nil || state != "failed" {
		t.Fatal("ordinary test retries became durable", state, err)
	}
}
func TestSourceApplyLastAttemptCrashReconciles(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := jobs.Repository{DB: db}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"source.apply", "source.clear", "source.pool_switch", "source.policy_apply"} {
		var job id.ID
		if err := db.WithinTx(ctx, func(tx pgx.Tx) error {
			var err error
			job, err = jobs.Enqueue(ctx, tx, jobs.Input{Kind: kind, IdempotencyKey: "durable-" + kind, Payload: []byte(`{"intent":"fixture"}`)})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		lease, err := repo.Claim(ctx, kind)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, "UPDATE jobs SET attempt=max_attempts,lease_expires_at=$2 WHERE id=$1", job, time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		taken, err := repo.Claim(ctx, kind)
		if err != nil || taken.ID != job || taken.FencingToken == lease.FencingToken {
			t.Fatal("durable source intent dropped at last attempt", kind, err)
		}
	}
}

// Controlled external peer; assertions below inspect persistent domain outcomes
// and physical stream state. Fixed-image CI covers the real ZLM implementation.
type controlledMedia struct {
	mu           sync.Mutex
	urls         map[string]string
	recording    map[string]bool
	failPath     string
	failURL      string
	AfterStart   func()
	FrozenFrames int64
	AddCalls     int
}

func newControlledMedia() *controlledMedia {
	return &controlledMedia{urls: map[string]string{}, recording: map[string]bool{}}
}
func (m *controlledMedia) AddProxy(ctx context.Context, in zlm.ProxyInput) (zlm.ProxyRef, error) {
	if err := ctx.Err(); err != nil {
		return zlm.ProxyRef{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AddCalls++
	u, err := url.Parse(in.URL)
	if err != nil {
		return zlm.ProxyRef{}, err
	}
	if u.Path == m.failPath || in.URL == m.failURL {
		return zlm.ProxyRef{}, zlm.ErrMediaOperation
	}
	m.urls[in.Key.Stream] = in.URL
	return zlm.ProxyRef{Key: in.Key, OpaqueKey: in.Key.VHost + "/" + in.Key.App + "/" + in.Key.Stream}, nil
}
func (m *controlledMedia) RemoveProxy(ctx context.Context, ref zlm.ProxyRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.urls, ref.Key.Stream)
	delete(m.recording, ref.Key.Stream)
	return nil
}
func (m *controlledMedia) Inspect(ctx context.Context, key zlm.StreamKey) (zlm.StreamSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return zlm.StreamSnapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.urls[key.Stream]; !ok {
		return zlm.StreamSnapshot{}, zlm.ErrStreamAbsent
	}
	frames := time.Now().UnixMilli()
	if m.FrozenFrames > 0 {
		frames = m.FrozenFrames
	}
	return zlm.StreamSnapshot{Key: key, Recording: m.recording[key.Stream], BytesPerSecond: 1 << 20, ObservedAt: time.Now().UTC(), Tracks: []zlm.Track{{Codec: "H264", Ready: true, Width: 320, Height: 180, FPS: 5, Frames: frames}}}, nil
}
func (m *controlledMedia) StartRecord(ctx context.Context, key zlm.StreamKey, _ string, _ int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.urls[key.Stream]; !ok {
		return zlm.ErrMediaOperation
	}
	m.recording[key.Stream] = true
	if m.AfterStart != nil {
		m.AfterStart()
	}
	return nil
}
func (m *controlledMedia) StopRecord(ctx context.Context, key zlm.StreamKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recording[key.Stream] = false
	return nil
}

type controlledProbe struct {
	media       *controlledMedia
	file        recording.Probe
	BeforeFrame func()
}

func (p controlledProbe) FirstFrame(ctx context.Context, raw string) (probe.VideoEvidence, error) {
	if p.BeforeFrame != nil {
		p.BeforeFrame()
	}
	if err := ctx.Err(); err != nil {
		return probe.VideoEvidence{}, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return probe.VideoEvidence{}, err
	}
	key := strings.TrimPrefix(u.Path, "/one_nvr/")
	p.media.mu.Lock()
	source := p.media.urls[key]
	p.media.mu.Unlock()
	if source == "" {
		return probe.VideoEvidence{}, probe.ErrProbeFailed
	}
	return probe.VideoEvidence{FirstFrame: true, Codec: "H264", Width: 320, Height: 180, FPS: 5, ObservedAt: time.Now().UTC()}, nil
}
func (p controlledProbe) InspectMP4(ctx context.Context, f *os.File) (probe.FileEvidence, error) {
	return p.file.InspectMP4(ctx, f)
}
func prepareControlledSourceTest(t *testing.T) (publishFixture, *controlledMedia, channel.Change, id.ID) {
	t.Helper()
	f := newPublicationFixture(t)
	ctx := context.Background()
	network, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	sources := channel.NewSources(f.DB, f.Auth, f.Site.Secrets, network)
	f.Service.Sources = sources
	f.Service.FreshNetwork = func(context.Context) (channel.NetworkPolicy, error) { return network, nil }
	f.Service.ProbeToken = "isolated-private-probe"
	media := newControlledMedia()
	f.Service.Media = media
	f.Service.Probe = controlledProbe{media: media, file: f.Service.Probe}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2 WHERE id=$1", f.Channel, f.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_runs SET state='recording' WHERE id=$1", f.Run); err != nil {
		t.Fatal(err)
	}
	var oldSession id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT stream_session_id FROM recording_runs WHERE id=$1", f.Run).Scan(&oldSession); err != nil {
		t.Fatal(err)
	}
	media.urls[string(oldSession)] = "rtsp://192.168.33.20/main"
	media.recording[string(oldSession)] = true
	draft, err := sources.CreateDraft(ctx, f.Admin, f.Channel, 2, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.21", MainPath: "/main", SubPath: "/sub-bad"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
	if err != nil {
		t.Fatal(err)
	}
	change, err := sources.RequestTest(ctx, f.Admin, f.Channel, draft.ID, "controlled-source-test")
	if err != nil {
		t.Fatal(err)
	}
	return f, media, change, draft.ID
}
func TestSourceTestDoesNotInterruptOldRun(t *testing.T) {
	f, media, change, revision := prepareControlledSourceTest(t)
	ctx := context.Background()
	media.failPath = "/sub-bad"
	repo := jobs.Repository{DB: f.DB}
	lease, err := repo.Claim(ctx, "source.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, lease, f.Service.ExecuteSourceTest); err != nil {
		t.Fatal(err)
	}
	var testID id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM source_tests WHERE job_id=$1", change.JobID).Scan(&testID); err != nil {
		t.Fatal(err)
	}
	result, err := f.Service.Sources.TestResult(ctx, f.Admin, f.Channel, testID)
	if err != nil || result.State != "succeeded" || !result.Main.FirstFrame || result.Main.State != "healthy" || result.Sub.State != "unavailable" || result.RevisionID != revision {
		t.Fatal("actual test/degraded sub result incorrect", result, err)
	}
	if result.ExpiresAt == nil || result.ObservedAt == nil || result.ExpiresAt.Sub(*result.ObservedAt) != 5*time.Minute {
		t.Fatal("source evidence lifetime differs")
	}
	var mediaProof bool
	if err := f.DB.Pool.QueryRow(ctx, "SELECT (result->'main'->>'first_frame')::boolean FROM source_tests WHERE id=$1", testID).Scan(&mediaProof); err != nil || !mediaProof {
		t.Fatal("completed test cannot supply pool-check evidence", mediaProof, err)
	}

	var current id.ID
	var runState string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", f.Channel).Scan(&current); err != nil || current != f.Revision {
		t.Fatal("test activated draft", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&runState); err != nil || runState != "recording" {
		t.Fatal("test interrupted old recording", runState, err)
	}
	var temporary int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM stream_sessions WHERE purpose='test' AND state NOT IN ('closed','failed')").Scan(&temporary); err != nil || temporary != 0 {
		t.Fatal("temporary test sessions retained", temporary, err)
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	if len(media.urls) != 1 {
		t.Fatal("test retained an upstream proxy")
	}
	for key := range media.urls {
		if !media.recording[key] {
			t.Fatal("old physical recorder interrupted")
		}
	}
}
func TestSourceTestRevokedBeforeStartDoesNotConnect(t *testing.T) {
	f, media, _, _ := prepareControlledSourceTest(t)
	ctx := context.Background()
	if err := f.Auth.SetGrants(ctx, f.Admin, f.Admin.UserID, 1, nil); err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: f.DB}
	lease, err := repo.Claim(ctx, "source.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, lease, f.Service.ExecuteSourceTest); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state,error_code FROM source_tests WHERE job_id=$1", lease.ID).Scan(&state, &reason); err != nil || state != "cancelled" || reason != "authorization_revoked" {
		t.Fatal("revoked test not cancelled", state, reason, err)
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	if len(media.urls) != 1 {
		t.Fatal("revoked test connected to draft")
	}
}

func TestSourceTestLastAttemptCrashRetainsCleanupIntent(t *testing.T) {
	f, media, change, _ := prepareControlledSourceTest(t)
	ctx := context.Background()
	fileProbe := f.Service.Probe.(controlledProbe).file
	f.Service.Probe = controlledProbe{media: media, file: fileProbe, BeforeFrame: func() {
		if _, err := f.DB.Pool.Exec(ctx, "UPDATE jobs SET attempt=max_attempts,lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", change.JobID); err != nil {
			t.Error(err)
		}
	}}
	repo := jobs.Repository{DB: f.DB}
	lease, err := repo.Claim(ctx, "source.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, lease, f.Service.ExecuteSourceTest); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatal("expired test worker did not lose ownership", err)
	}
	if _, err := repo.Claim(ctx, "source.test"); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatal("finite test unexpectedly reclaimed", err)
	}
	if err := f.Service.CleanupSourceTests(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM source_tests WHERE job_id=$1", change.JobID).Scan(&state); err != nil || state != "failed" {
		t.Fatal("expired test diagnostic not terminal", state, err)
	}
	var open int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM stream_sessions WHERE source_test_id IS NOT NULL AND state NOT IN ('closed','failed')").Scan(&open); err != nil || open != 0 {
		t.Fatal("expired test lost durable cleanup", open, err)
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	if len(media.urls) != 1 {
		t.Fatal("expired test retained proxy or removed old source")
	}
}

func TestSourceTestExhaustedBeforeSessionBecomesTerminal(t *testing.T) {
	f, _, change, _ := prepareControlledSourceTest(t)
	ctx := context.Background()
	repo := jobs.Repository{DB: f.DB}
	if _, err := repo.Claim(ctx, "source.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE jobs SET attempt=max_attempts,lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", change.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Claim(ctx, "source.test"); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatal(err)
	}
	if err := f.Service.CleanupSourceTests(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := f.Service.Sources.TestResult(ctx, f.Admin, f.Channel, *change.TestID)
	if err != nil || result.State != "failed" {
		t.Fatal("exhausted no-session test still queued", result.State, err)
	}
}

func TestSourceTestsSameChannelWaitWithoutConsumingAttempt(t *testing.T) {
	f, _, _, revision := prepareControlledSourceTest(t)
	ctx := context.Background()
	second, err := f.Service.Sources.RequestTest(ctx, f.Admin, f.Channel, revision, "same-channel-next")
	if err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: f.DB}
	first, err := repo.Claim(ctx, "source.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Claim(ctx, "source.test"); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatal("same-channel source test claimed while prior lease active", err)
	}
	var attempt int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT attempt FROM jobs WHERE id=$1", second.JobID).Scan(&attempt); err != nil || attempt != 0 {
		t.Fatal("channel serialization consumed waiting attempt", attempt, err)
	}
	if err := jobs.Execute(ctx, repo, first, f.Service.ExecuteSourceTest); err != nil {
		t.Fatal(err)
	}
	next, err := repo.Claim(ctx, "source.test")
	if err != nil || next.ID != second.JobID {
		t.Fatal("channel capacity not released", err)
	}
}

func TestSourceTestProvidesSeparatedBitrateEvidence(t *testing.T) {
	f, _, change, _ := prepareControlledSourceTest(t)
	ctx := context.Background()
	repo := jobs.Repository{DB: f.DB}
	lease, err := repo.Claim(ctx, "source.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, lease, f.Service.ExecuteSourceTest); err != nil {
		t.Fatal(err)
	}
	var samples int
	var seconds float64
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*),coalesce(extract(epoch FROM max(observed_at)-min(observed_at)),0)::double precision FROM recording_bitrate_samples WHERE source_test_id=$1 AND valid", change.TestID).Scan(&samples, &seconds); err != nil || samples < 2 || seconds < 10 {
		t.Fatal("actual source test lacks separated valid bitrate evidence", samples, seconds, err)
	}
}
