package integration

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/site"
	"sync"
	"testing"
	"time"
)

const testPassword = "fixture-password-12345"

func authFixture(t *testing.T, dataDirectories ...string) (*database.DB, *auth.Service, *site.Service, auth.LoginResult) {
	t.Helper()
	db := testDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	dataDirectory := ""
	if len(dataDirectories) > 0 {
		dataDirectory = dataDirectories[0]
	} else {
		dataDirectory = t.TempDir()
	}
	secret, err := secrets.Init(dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	hasher := auth.NewPasswordHasher(2)
	accounts := auth.NewService(db, secret, hasher)
	if err := accounts.ApplyEntryProtocol(ctx, "http"); err != nil {
		t.Fatal(err)
	}
	sites := &site.Service{DB: db, Secrets: secret, Passwords: hasher}
	if _, err := sites.Setup(ctx, site.SetupInput{Token: secret.SetupToken, AdminName: "admin", AdminPassword: testPassword, Name: "Test site", Timezone: "Asia/Shanghai", ChannelCount: 16}); err != nil {
		t.Fatal(err)
	}
	login, err := accounts.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return db, accounts, sites, login
}
func TestConcurrentSetupConsumesTokenOnce(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	secret, err := secrets.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := site.Service{DB: db, Secrets: secret, Passwords: auth.NewPasswordHasher(2)}
	input := site.SetupInput{Token: secret.SetupToken, AdminName: "admin", AdminPassword: testPassword, Name: "Site", Timezone: "Asia/Shanghai", ChannelCount: 16}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := svc.Setup(ctx, input); results <- err }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, site.ErrInitialized) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("setup successes=%d", successes)
	}
	for table, want := range map[string]int{"sites": 1, "users": 1, "channels": 16, "channel_grants": 16, "recording_policies": 16} {
		var got int
		if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s rows=%d %v", table, got, err)
		}
	}
	if _, err := svc.Setup(ctx, input); !errors.Is(err, site.ErrInitialized) {
		t.Fatal("token reused")
	}
}
func TestSessionExpiryAndRevocation(t *testing.T) {
	db, svc, _, login := authFixture(t)
	ctx := context.Background()
	p, err := svc.Authenticate(ctx, login.RawSession)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE sessions SET last_seen_at=clock_timestamp()-interval '31 minutes' WHERE id=$1", p.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, login.RawSession); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("idle session accepted")
	}
	login, err = svc.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE sessions SET absolute_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", login.Principal.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, login.RawSession); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("absolute expired session accepted")
	}
	login, err = svc.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, login.Principal); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, login.RawSession); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("logout not revoked")
	}
	login, err = svc.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangePassword(ctx, login.Principal, testPassword, "replacement-password-12345"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, login.RawSession); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("changed password kept session")
	}
	if _, err := svc.Login(ctx, "admin", testPassword); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("old password accepted")
	}
}
func TestDirectUnauthorizedChannelDenied(t *testing.T) {
	db, svc, _, admin := authFixture(t)
	ctx := context.Background()
	viewer, err := svc.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "viewer", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := svc.Login(ctx, "viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	var channel id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequireChannel(ctx, login.Principal, channel, auth.Live); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("ungranted channel exposed")
	}
	if err := svc.SetGrants(ctx, admin.Principal, viewer.ID, viewer.Version, []auth.Grant{{ChannelID: channel, Actions: []auth.Action{auth.Live}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, login.RawSession); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("grant change did not revoke old session")
	}
	login, err = svc.Login(ctx, "viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RequireChannel(ctx, login.Principal, channel, auth.Live); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequireChannel(ctx, login.Principal, channel, auth.Configure); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer configured channel")
	}
	if _, err := db.Pool.Exec(ctx, "DELETE FROM channel_grants WHERE user_id=$1", admin.Principal.UserID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequireChannel(ctx, admin.Principal, channel, auth.Live); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("admin bypassed channel grant")
	}
}
func TestLastAdminProtectedAndAuditFailureRollsBack(t *testing.T) {
	db, svc, _, admin := authFixture(t)
	ctx := context.Background()
	disabled := false
	if _, err := svc.UpdateUser(ctx, admin.Principal, admin.User.ID, admin.User.Version, auth.UpdateUserInput{Enabled: &disabled}); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatal("last admin disabled")
	}
	role := "viewer"
	if _, err := svc.UpdateUser(ctx, admin.Principal, admin.User.ID, admin.User.Version, auth.UpdateUserInput{Role: &role}); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatal("last admin demoted")
	}
	user, err := svc.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "operator", Password: testPassword, Role: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	old, err := svc.Login(ctx, "operator", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	// A real SQL failure at audit insertion must roll back user and session changes.
	if err := db.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "ALTER TABLE audit_logs ADD CONSTRAINT reject_audit CHECK (false) NOT VALID")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateUser(ctx, admin.Principal, user.ID, user.Version, auth.UpdateUserInput{Enabled: &disabled}); err == nil {
		t.Fatal("audit failure reported success")
	}
	if _, err := svc.Authenticate(ctx, old.RawSession); err != nil {
		t.Fatal("failed transaction revoked session")
	}
	if _, err := db.Pool.Exec(ctx, "ALTER TABLE audit_logs DROP CONSTRAINT reject_audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateUser(ctx, admin.Principal, user.ID, user.Version, auth.UpdateUserInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, old.RawSession); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("disabled user retained session")
	}
}

func TestConcurrentAdminDemotionLeavesOneEnabledAdmin(t *testing.T) {
	db, svc, _, first := authFixture(t)
	ctx := context.Background()
	second, err := svc.CreateUser(ctx, first.Principal, auth.CreateUserInput{Username: "second", Password: testPassword, Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := svc.Login(ctx, "second", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	role := "viewer"
	results := make(chan error, 2)
	for _, entry := range []struct {
		p auth.Principal
		u auth.User
	}{{first.Principal, first.User}, {login.Principal, second}} {
		go func(p auth.Principal, u auth.User) {
			_, err := svc.UpdateUser(ctx, p, u.ID, u.Version, auth.UpdateUserInput{Role: &role})
			results <- err
		}(entry.p, entry.u)
	}
	successes := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, auth.ErrLastAdmin) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("concurrent demotions not serialized")
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE role='admin' AND enabled").Scan(&count); err != nil || count != 1 {
		t.Fatal("last enabled admin lost")
	}
}

func TestFailedLoginAuditedWithoutPasswordOrSession(t *testing.T) {
	db, svc, _, _ := authFixture(t)
	ctx := context.Background()
	var before int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM sessions").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "admin", "wrong-password"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("invalid credentials accepted")
	}
	var count int
	var details string
	if err := db.Pool.QueryRow(ctx, "SELECT count(*),coalesce(min(details::text),'') FROM audit_logs WHERE action='auth.login_failed'").Scan(&count, &details); err != nil || count != 1 {
		t.Fatalf("failed login audit %d %v", count, err)
	}
	if details != "{}" {
		t.Fatal("failed login audit included input")
	}
	var after int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM sessions").Scan(&after); err != nil || before != after {
		t.Fatal("failed login created session")
	}
}

func TestRoleCeilingRejectsViewerConfigureAndPrunesOnDemotion(t *testing.T) {
	db, svc, _, admin := authFixture(t)
	ctx := context.Background()
	viewer, err := svc.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "viewer", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	var channel id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetGrants(ctx, admin.Principal, viewer.ID, viewer.Version, []auth.Grant{{ChannelID: channel, Actions: []auth.Action{auth.Configure}}}); !errors.Is(err, auth.ErrInvalid) {
		t.Fatalf("viewer configure grant accepted %v", err)
	}
	second, err := svc.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "second", Password: testPassword, Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetGrants(ctx, admin.Principal, second.ID, second.Version, []auth.Grant{{ChannelID: channel, Actions: []auth.Action{auth.Live, auth.Configure}}}); err != nil {
		t.Fatal(err)
	}
	role := "viewer"
	if _, err := svc.UpdateUser(ctx, admin.Principal, second.ID, second.Version+1, auth.UpdateUserInput{Role: &role}); err != nil {
		t.Fatal(err)
	}
	login, err := svc.Login(ctx, "second", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RequireChannel(ctx, login.Principal, channel, auth.Configure); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("demoted admin retained configure")
	}
	var configure bool
	if err := db.Pool.QueryRow(ctx, "SELECT configure FROM channel_grants WHERE user_id=$1 AND channel_id=$2", second.ID, channel).Scan(&configure); err != nil || configure {
		t.Fatal("forbidden grants not pruned")
	}
	// Historical/tampered grants must still be capped at authorization time.
	if _, err := db.Pool.Exec(ctx, "UPDATE channel_grants SET configure=true WHERE user_id=$1", second.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequireChannel(ctx, login.Principal, channel, auth.Configure); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("read-side ceiling missing")
	}
}

func TestCreationAndAdminRevocationCommitInOrder(t *testing.T) {
	db, svc, _, first := authFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	second, err := svc.CreateUser(ctx, first.Principal, auth.CreateUserInput{Username: "second", Password: testPassword, Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := svc.Login(ctx, "second", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := db.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	if _, err := blocker.Exec(ctx, "SELECT pg_advisory_lock(999001)"); err != nil {
		t.Fatal(err)
	}
	unlock := func() { _, _ = blocker.Exec(context.Background(), "SELECT pg_advisory_unlock(999001)") }
	defer unlock()
	if _, err := db.Pool.Exec(ctx, `CREATE FUNCTION pause_creation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.username='paused_admin' THEN PERFORM pg_advisory_xact_lock(999001); END IF; RETURN NEW; END; $$; CREATE TRIGGER pause_creation BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION pause_creation()`); err != nil {
		t.Fatal(err)
	}
	created := make(chan error, 1)
	go func() {
		_, err := svc.CreateUser(ctx, login.Principal, auth.CreateUserInput{Username: "paused_admin", Password: testPassword, Role: "admin"})
		created <- err
	}()
	wait := func(query string) bool {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var count int
			if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE $1", query).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count > 0 {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	if !wait("INSERT INTO users%") {
		t.Fatal("creation did not reach controlled pause")
	}
	role := "viewer"
	demoted := make(chan error, 1)
	go func() {
		_, err := svc.UpdateUser(ctx, first.Principal, second.ID, second.Version, auth.UpdateUserInput{Role: &role})
		demoted <- err
	}()
	if !wait("SELECT pg_advisory_xact_lock(170018)%") {
		unlock()
		<-created
		err := <-demoted
		t.Fatalf("revocation committed ahead of in-flight authorized creation, err=%v", err)
	}
	unlock()
	if err := <-created; err != nil {
		t.Fatal(err)
	}
	if err := <-demoted; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, login.RawSession); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("revoked admin session still accepted")
	}
	if _, err := svc.CreateUser(ctx, login.Principal, auth.CreateUserInput{Username: "forbidden_admin", Password: testPassword, Role: "admin"}); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("revoked principal created administrator")
	}
}
