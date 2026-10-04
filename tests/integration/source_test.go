package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
)

const sourcePassword = "camera-credential-fixture-marker"

func stringPointer(s string) *string { return &s }

func TestSourceDraftImmutabilityAndKeep(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	policy, err := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := channel.NewSources(db, accounts, sites.Secrets, policy)
	var channelID id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&channelID); err != nil {
		t.Fatal(err)
	}
	in := channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, Credentials: channel.CredentialInput{Username: stringPointer("fixture-user"), Password: stringPointer(sourcePassword), PasswordAction: "replace"}, IdentityIntent: "replace"}
	rev, err := svc.CreateDraft(ctx, admin.Principal, channelID, 1, in)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Config.RTSPPort != 554 || rev.Config.Transport != "tcp" || rev.PasswordState != "saved" {
		t.Fatal("source defaults lost")
	}
	var current *id.ID
	var version int64
	if err := db.Pool.QueryRow(ctx, "SELECT current_revision_id,version FROM channels WHERE id=$1", channelID).Scan(&current, &version); err != nil {
		t.Fatal(err)
	}
	if current != nil || version != 2 {
		t.Fatal("draft activated or version not guarded")
	}
	plain, err := svc.Reveal(ctx, admin.Principal, channelID, rev.ID)
	if err != nil || plain.Password != sourcePassword || plain.Username != "fixture-user" {
		t.Fatal("reveal mismatch", err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2 WHERE id=$1", channelID, rev.ID); err != nil {
		t.Fatal(err)
	} // persisted current fixture; apply workflow belongs to Task6
	in.IdentityIntent = "modify"
	in.Config.MainPath = "/next"
	in.Credentials = channel.CredentialInput{PasswordAction: "keep"}
	next, err := svc.CreateDraft(ctx, admin.Principal, channelID, 2, in)
	if err != nil {
		t.Fatal(err)
	}
	if next.SourceID != rev.SourceID || next.Number != 2 {
		t.Fatal("same-device identity changed")
	}
	plain, err = svc.Reveal(ctx, admin.Principal, channelID, next.ID)
	if err != nil || plain.Password != sourcePassword {
		t.Fatal("keep failed under new AAD", err)
	}
	if _, err := svc.CreateDraft(ctx, admin.Principal, channelID, 2, in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale version saved", err)
	}
	page, err := svc.ListRevisions(ctx, admin.Principal, channelID, "", 100)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("source history missing", err)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), sourcePassword) || strings.Contains(string(raw), "fixture-user") {
		t.Fatal("normal revision DTO leaked credentials")
	}
	var stored []byte
	if err := db.Pool.QueryRow(ctx, "SELECT password_ciphertext FROM source_revisions WHERE id=$1", next.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), sourcePassword) {
		t.Fatal("DB stored plaintext")
	}
}

func TestSourceDraftIdempotency(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	svc := channel.NewSources(db, accounts, sites.Secrets, policy)
	var c id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&c); err != nil {
		t.Fatal(err)
	}
	in := channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, Credentials: channel.CredentialInput{Password: stringPointer(sourcePassword), PasswordAction: "replace"}, IdentityIntent: "replace"}
	one, err := svc.CreateDraft(ctx, admin.Principal, c, 1, in, "fixture-request")
	if err != nil {
		t.Fatal(err)
	}
	two, err := svc.CreateDraft(ctx, admin.Principal, c, 1, in, "fixture-request")
	if err != nil || one.ID != two.ID {
		t.Fatal("retry created another revision or conflicted", err)
	}
	in.Credentials.Password = stringPointer("different-fixture-secret")
	if _, err := svc.CreateDraft(ctx, admin.Principal, c, 1, in, "fixture-request"); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("idempotent request changed meaning", err)
	}
}

func TestCredentialRevealAuditFailure(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	svc := channel.NewSources(db, accounts, sites.Secrets, policy)
	var channelID id.ID
	db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&channelID)
	rev, err := svc.CreateDraft(ctx, admin.Principal, channelID, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, Credentials: channel.CredentialInput{Username: stringPointer("fixture-user"), Password: stringPointer(sourcePassword), PasswordAction: "replace"}, IdentityIntent: "replace"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `CREATE FUNCTION fail_reveal_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='source.credentials_revealed' THEN RAISE EXCEPTION 'fixture audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reveal_audit_failure BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION fail_reveal_audit()`); err != nil {
		t.Fatal(err)
	}
	plain, err := svc.Reveal(ctx, admin.Principal, channelID, rev.ID)
	if err == nil || plain.Password != "" || plain.Username != "" {
		t.Fatal("audit failure delivered credential")
	}
}

func TestSourceExportSnapshotAuthorization(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	svc := channel.NewSources(db, accounts, sites.Secrets, policy)
	var channels []id.ID
	rows, err := db.Pool.Query(ctx, "SELECT id FROM channels ORDER BY channel_no LIMIT 2")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c id.ID
		rows.Scan(&c)
		channels = append(channels, c)
	}
	rows.Close()
	for _, c := range channels {
		rev, err := svc.CreateDraft(ctx, admin.Principal, c, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, Credentials: channel.CredentialInput{Password: stringPointer(sourcePassword), PasswordAction: "replace"}, IdentityIntent: "replace"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2 WHERE id=$1", c, rev.ID); err != nil {
			t.Fatal(err)
		}
	}
	file, err := svc.Export(ctx, admin.Principal, channels)
	if err != nil || file.FormatVersion != 1 || len(file.Channels) != 2 || file.Channels[0].Password != sourcePassword {
		t.Fatal("export missing consistent decrypted source", err)
	}
	user, err := accounts.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "source_operator", Password: testPassword, Role: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetGrants(ctx, admin.Principal, user.ID, 1, []auth.Grant{{ChannelID: channels[0], Actions: []auth.Action{auth.Configure}}}); err != nil {
		t.Fatal(err)
	}
	login, err := accounts.Login(ctx, "source_operator", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Export(ctx, login.Principal, []id.ID{channels[0]}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("operator exported credential", err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=NULL WHERE id=$1", channels[1]); err != nil {
		t.Fatal(err)
	}
	file, err = svc.Export(ctx, admin.Principal, channels)
	if err != nil || len(file.Channels) != 2 {
		t.Fatal("unconfigured slot prevented export", err)
	}
	raw, _ := json.Marshal(file)
	if !strings.Contains(string(raw), `"source_state":"not_configured"`) {
		t.Fatal("unconfigured slot not marked")
	}
	var original id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM source_revisions WHERE channel_id=$1", channels[1]).Scan(&original); err != nil {
		t.Fatal(err)
	}
	bad, _ := id.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO source_revisions(id,channel_id,source_id,revision_no,ip,main_path,credential_key_id,username_nonce,username_ciphertext,password_nonce,password_ciphertext) SELECT $2,channel_id,source_id,2,ip,main_path,credential_key_id,username_nonce,username_ciphertext,password_nonce,password_ciphertext FROM source_revisions WHERE id=$1`, original, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2 WHERE id=$1", channels[1], bad); err != nil {
		t.Fatal(err)
	}
	file, err = svc.Export(ctx, admin.Principal, channels)
	if err == nil || len(file.Channels) != 0 {
		t.Fatal("one un-decryptable revision returned a partial plaintext export")
	}
}

func TestSourceDraftPasswordIntentAndOwnership(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	svc := channel.NewSources(db, accounts, sites.Secrets, policy)
	var c, other id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&c); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=2").Scan(&other); err != nil {
		t.Fatal(err)
	}
	in := channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, Credentials: channel.CredentialInput{PasswordAction: "keep"}, IdentityIntent: "replace"}
	if _, err := svc.CreateDraft(ctx, admin.Principal, c, 1, in); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("new source kept nonexistent password", err)
	}
	in.Credentials = channel.CredentialInput{PasswordAction: "replace", Password: stringPointer("******")}
	if _, err := svc.CreateDraft(ctx, admin.Principal, c, 1, in); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("mask saved as password", err)
	}
	in.Credentials = channel.CredentialInput{PasswordAction: "clear"}
	rev, err := svc.CreateDraft(ctx, admin.Principal, c, 1, in)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := svc.Reveal(ctx, admin.Principal, c, rev.ID)
	if err != nil || plain.Password != "" || rev.PasswordState != "empty" {
		t.Fatal("explicit empty password not preserved", err)
	}
	in.IdentityIntent = "history"
	in.HistorySourceID = rev.SourceID
	if _, err := svc.CreateDraft(ctx, admin.Principal, other, 1, in); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("cross-channel history reused", err)
	}
	badSecret := sites.Secrets
	badSecret.MasterKey = strings.Repeat("ab", 32)
	in.IdentityIntent = "replace"
	in.HistorySourceID = ""
	if _, err := channel.NewSources(db, accounts, badSecret, policy).CreateDraft(ctx, admin.Principal, other, 1, in); err == nil {
		t.Fatal("changed master key silently saved mixed credentials")
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM source_revisions WHERE channel_id=$1", other).Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid drafts changed source history", err)
	}
}

func TestSourceCredentialAuthorizationAndExportAudit(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	svc := channel.NewSources(db, accounts, sites.Secrets, policy)
	var c id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&c); err != nil {
		t.Fatal(err)
	}
	rev, err := svc.CreateDraft(ctx, admin.Principal, c, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, Credentials: channel.CredentialInput{PasswordAction: "replace", Password: stringPointer(sourcePassword)}, IdentityIntent: "replace"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2 WHERE id=$1", c, rev.ID); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"admin", "operator", "viewer"} {
		u, err := accounts.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "source_" + role, Password: testPassword, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		actions := []auth.Action{auth.Live}
		if role == "operator" {
			actions = append(actions, auth.Configure)
		}
		if err := accounts.SetGrants(ctx, admin.Principal, u.ID, 1, []auth.Grant{{ChannelID: c, Actions: actions}}); err != nil {
			t.Fatal(err)
		}
		login, err := accounts.Login(ctx, "source_"+role, testPassword)
		if err != nil {
			t.Fatal(err)
		}
		if plain, err := svc.Reveal(ctx, login.Principal, c, rev.ID); !errors.Is(err, auth.ErrForbidden) || plain.Password != "" {
			t.Fatal("unauthorized reveal", role, err)
		}
		if file, err := svc.Export(ctx, login.Principal, []id.ID{c}); !errors.Is(err, auth.ErrForbidden) || len(file.Channels) != 0 {
			t.Fatal("unauthorized export", role, err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `CREATE FUNCTION fail_export_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='source.config_exported' THEN RAISE EXCEPTION 'fixture audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER export_audit_failure BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION fail_export_audit()`); err != nil {
		t.Fatal(err)
	}
	if file, err := svc.Export(ctx, admin.Principal, []id.ID{c}); err == nil || len(file.Channels) != 0 {
		t.Fatal("audit failure returned plaintext file")
	}
	rows, err := db.Pool.Query(ctx, "SELECT details::text FROM audit_logs UNION ALL SELECT payload::text FROM jobs")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, sourcePassword) {
			t.Fatal("audit or job leaked credential")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
