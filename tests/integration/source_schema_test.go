package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"

	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/migrations"
)

// Removing preservation or the channel-bound foreign keys must fail these
// tests against PostgreSQL, independently of service-level validation.
func TestSourceSchemaPreservesFoundation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, "CREATE TABLE schema_migrations(name text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT clock_timestamp())"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_foundation.sql", "0002_entry_protocol.sql", "0003_tls_watch.sql", "0004_gateway_results.sql"} {
		raw, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(raw)
		if _, err = db.Pool.Exec(ctx, "INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)", name, hex.EncodeToString(hash[:])); err != nil {
			t.Fatal(err)
		}
	}
	siteID, _ := id.New()
	userID, _ := id.New()
	channelID, _ := id.New()
	poolID, _ := id.New()
	statements := []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO sites(id,name,timezone,channel_count) VALUES($1,'Existing','Asia/Tokyo',16)", []any{siteID}},
		{"INSERT INTO users(id,site_id,username,password_hash,role) VALUES($1,$2,'admin','existing-hash','admin')", []any{userID, siteID}},
		{"INSERT INTO channels(id,site_id,channel_no,channel_name,version) VALUES($1,$2,1,'Old channel',9)", []any{channelID, siteID}},
		{"INSERT INTO channel_grants(user_id,channel_id,playback,configure) VALUES($1,$2,true,true)", []any{userID, channelID}},
		{"INSERT INTO recording_policies(channel_id,mode) VALUES($1,'none')", []any{channelID}},
		{"INSERT INTO storage_pools(id,site_id,name,canonical_path) VALUES($1,$2,'Old pool','/storage/existing')", []any{poolID, siteID}},
		{"INSERT INTO gateway_tls_state(singleton,source,state,version) VALUES(true,'manual','pending',7)", nil},
	}
	for _, q := range statements {
		if _, err := db.Pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version int64
	var name, zone, mode, path string
	var playback, configure, enabled bool
	err := db.Pool.QueryRow(ctx, `SELECT c.version,c.channel_name,s.timezone,p.mode,g.playback,g.configure,sp.canonical_path,c.enabled FROM channels c JOIN sites s ON s.id=c.site_id JOIN recording_policies p ON p.channel_id=c.id JOIN channel_grants g ON g.channel_id=c.id JOIN storage_pools sp ON sp.site_id=s.id WHERE c.id=$1 AND g.user_id=$2 AND sp.id=$3`, channelID, userID, poolID).Scan(&version, &name, &zone, &mode, &playback, &configure, &path, &enabled)
	if err != nil {
		t.Fatal(err)
	}
	if version != 9 || name != "Old channel" || zone != "Asia/Tokyo" || mode != "none" || !playback || !configure || path != "/storage/existing" || !enabled {
		t.Fatal("foundation configuration changed during upgrade")
	}
	var tlsVersion int
	if err := db.Pool.QueryRow(ctx, "SELECT version FROM gateway_tls_state").Scan(&tlsVersion); err != nil || tlsVersion != 7 {
		t.Fatalf("TLS changed: %d %v", tlsVersion, err)
	}
	if err := database.Ready(ctx, db); err != nil {
		t.Fatal(err)
	}
}

func TestSourceSchemaRejectsCrossChannelAndDuplicateRun(t *testing.T) {
	db, _, sites, _ := authFixture(t)
	ctx := context.Background()
	var channels []id.ID
	rows, err := db.Pool.Query(ctx, "SELECT id FROM channels ORDER BY channel_no LIMIT 2")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c id.ID
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		channels = append(channels, c)
	}
	rows.Close()
	if len(channels) != 2 {
		t.Fatal("fixture channels missing")
	}
	sourceID, _ := id.New()
	revisionID, _ := id.New()
	sessionID, _ := id.New()
	poolID, _ := id.New()
	runID, _ := id.New()
	segmentID, _ := id.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	reject := func(sql string, args ...any) {
		t.Helper()
		_, err := db.Pool.Exec(ctx, sql, args...)
		if err == nil {
			t.Error("invalid source ownership/duplicate accepted")
			return
		}
		var e *pgconn.PgError
		if !errors.As(err, &e) || (e.Code != "23503" && e.Code != "23505" && e.Code != "23502" && e.Code != "23514" && e.Code != "P0001") {
			t.Errorf("failed for unexpected reason: %v", err)
		}
	}
	exec("INSERT INTO source_identities(id,channel_id,label) VALUES($1,$2,'Original')", sourceID, channels[0])
	exec(`INSERT INTO source_revisions(id,channel_id,source_id,revision_no,ip,main_path,credential_key_id,username_nonce,username_ciphertext,password_nonce,password_ciphertext) VALUES($1,$2,$3,1,'192.168.33.20','/main','fixture-key',decode(repeat('00',12),'hex'),decode(repeat('00',16),'hex'),decode(repeat('00',12),'hex'),decode(repeat('00',16),'hex'))`, revisionID, channels[0], sourceID)
	reject("UPDATE channels SET current_revision_id=$2 WHERE id=$1", channels[1], revisionID)
	reject("UPDATE source_revisions SET main_path='/other' WHERE id=$1", revisionID)
	exec("UPDATE channels SET current_revision_id=$2 WHERE id=$1", channels[0], revisionID)
	exec("INSERT INTO storage_pools(id,site_id,name,canonical_path) VALUES($1,$2,'Pool','/storage/source-test')", poolID, sites.Secrets.SiteID)
	exec("INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose) VALUES($1::uuid,$2,$3,1,'one_nvr',$1::uuid::text,'main')", sessionID, channels[0], revisionID)
	exec("INSERT INTO recording_runs(id,channel_id,source_revision_id,stream_session_id,pool_id,site_id,work_relative_path,purpose) VALUES($1,$2,$3,$4,$5,$6,'.work/zlm/fixture','continuous')", runID, channels[0], revisionID, sessionID, poolID, sites.Secrets.SiteID)
	reject("UPDATE recording_runs SET pool_id=NULL WHERE id=$1", runID)
	secondRun, _ := id.New()
	reject("INSERT INTO recording_runs(id,channel_id,source_revision_id,stream_session_id,pool_id,site_id,work_relative_path,purpose) VALUES($1,$2,$3,$4,$5,$6,'.work/zlm/second','continuous')", secondRun, channels[0], revisionID, sessionID, poolID, sites.Secrets.SiteID)
	exec(`INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes) VALUES($1,$2,$3,$4,$5,'native/one.mp4','recordings/CH01/2026-10-04/one.mp4','2026-10-04T00:00:00Z','Asia/Shanghai',28800,'2026-10-04T00:00:00Z','2026-10-04T00:01:00Z',100)`, segmentID, channels[0], revisionID, runID, poolID)
	next, _ := id.New()
	reject("UPDATE recording_segments SET target_relative_path='recordings/CH01/renamed.mp4' WHERE id=$1", segmentID)
	reject(`INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes) SELECT $2,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes FROM recording_segments WHERE id=$1`, segmentID, next)
	exec("INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes) VALUES($1,$2,'recordings/CH01/one.mp4',100)", segmentID, poolID)
	reject("INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes) VALUES($1,$2,'recordings/CH01/one.mp4',100)", segmentID, poolID)
	job1, _ := id.New()
	job2, _ := id.New()
	switch1, _ := id.New()
	switch2, _ := id.New()
	for i, j := range []id.ID{job1, job2} {
		exec("INSERT INTO jobs(id,kind,object_id,idempotency_key,parameter_digest) VALUES($1,'source.apply',$2,$3,decode(repeat('00',32),'hex'))", j, channels[0], string([]rune{'a' + rune(i)}))
	}
	exec("INSERT INTO source_switches(id,channel_id,job_id,new_revision_id,expected_version) VALUES($1,$2,$3,$4,1)", switch1, channels[0], job1, revisionID)
	reject("INSERT INTO source_switches(id,channel_id,job_id,new_revision_id,expected_version) VALUES($1,$2,$3,$4,1)", switch2, channels[0], job2, revisionID)
	reject("DELETE FROM source_revisions WHERE id=$1", revisionID)
	reject("DELETE FROM source_identities WHERE id=$1", sourceID)
	reject("DELETE FROM storage_pools WHERE id=$1", poolID)
}
