package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"strings"
	"testing"
	"time"
)

func importFixture(t *testing.T) publishFixture {
	t.Helper()
	f := newPublicationFixture(t)
	network, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	f.Service.Sources = channel.NewSources(f.DB, f.Auth, f.Site.Secrets, network)
	if _, err := f.DB.Pool.Exec(context.Background(), "UPDATE channels SET current_revision_id=$2 WHERE id=$1", f.Channel, f.Revision); err != nil {
		t.Fatal(err)
	}
	return f
}
func TestExportRoundTripsForeignAndSameSite(t *testing.T) {
	f := importFixture(t)
	ctx := context.Background()
	exported, err := f.Service.Sources.Export(ctx, f.Admin, []id.ID{f.Channel})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(exported)
	preview, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "json", bytes.NewReader(raw))
	if err != nil || len(preview.Items) != 1 || preview.Items[0].ChannelID == nil || *preview.Items[0].ChannelID != f.Channel {
		t.Fatal("same site source export cannot preview", preview, err)
	}
	if preview.ExpiresAt.Before(time.Now().Add(29*time.Minute)) || len(preview.Items[0].Errors) != 0 {
		t.Fatal("preview invalid", preview)
	}
	exported.Channels[0].ChannelNo = 2
	wrong, _ := json.Marshal(exported)
	if _, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "json", bytes.NewReader(wrong)); err == nil {
		t.Fatal("same-site mismatched UUID and number admitted")
	}
	foreign, _ := id.New()
	external, _ := id.New()
	exported.SiteID = foreign
	exported.Channels[0].ChannelID = external
	other, _ := json.Marshal(exported)
	preview, err = f.Service.Sources.PreviewImport(ctx, f.Admin, "json", bytes.NewReader(other))
	if err != nil || preview.Items[0].ChannelID == nil || *preview.Items[0].ChannelID == external || len(preview.Items[0].Warnings) == 0 {
		t.Fatal("foreign identity injected", preview, err)
	}
	var local id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=2").Scan(&local); err != nil || *preview.Items[0].ChannelID != local {
		t.Fatal("foreign mapping ignored local number", err)
	}
}
func TestImportPreviewEncryptedDoesNotApplyAndSkipsEmpty(t *testing.T) {
	f := importFixture(t)
	ctx := context.Background()
	secret := "private-import-secret"
	input := "channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Front,192.168.33.24,554,admin," + secret + ",/main,/sub\n2,Empty,,,admin,,/main,\n"
	preview, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "csv", strings.NewReader(input))
	if err != nil || len(preview.Items) != 2 {
		t.Fatal(err)
	}
	var summary, cipher []byte
	if err := f.DB.Pool.QueryRow(ctx, "SELECT summary::text::bytea,draft_ciphertext FROM source_import_items WHERE batch_id=$1 AND row_no=1", preview.BatchID).Scan(&summary, &cipher); err != nil || bytes.Contains(summary, []byte(secret)) || bytes.Contains(cipher, []byte(secret)) {
		t.Fatal("plaintext import persisted", err)
	}
	response, _ := json.Marshal(preview)
	if bytes.Contains(response, []byte(secret)) || bytes.Contains(response, []byte("password\":")) {
		t.Fatal("preview exposed password")
	}
	var current id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", f.Channel).Scan(&current); err != nil || current != f.Revision {
		t.Fatal("preview applied source", err)
	}
	exported, err := f.Service.Sources.Export(ctx, f.Admin, []id.ID{f.Channel})
	if err != nil {
		t.Fatal(err)
	}
	exported.Channels[0].SourceState = "not_configured"
	exported.Channels[0].IP = ""
	exported.Channels[0].MainPath = ""
	exported.Channels[0].SubPath = ""
	exported.Channels[0].Username = ""
	exported.Channels[0].Password = ""
	raw, _ := json.Marshal(exported)
	empty, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "json", bytes.NewReader(raw))
	if err != nil || empty.Items[0].State != "skipped" {
		t.Fatal("empty export row implicitly cleared source", empty, err)
	}
}

func TestImportExpiryRevocationAndPartialRecovery(t *testing.T) {
	f := importFixture(t)
	ctx := context.Background()
	exported, err := f.Service.Sources.Export(ctx, f.Admin, []id.ID{f.Channel})
	if err != nil {
		t.Fatal(err)
	}
	exported.Channels[0].ChannelName = "Imported name"
	raw, _ := json.Marshal(exported)
	preview, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	sel := channel.ImportSelection{Row: 1, ChannelID: f.Channel, ExpectedVersion: *preview.Items[0].ExpectedVersion, IdentityIntent: "modify", PasswordAction: "clear", ImportName: true}
	change, err := f.Service.Sources.SubmitImport(ctx, f.Admin, preview.BatchID, []channel.ImportSelection{sel}, "import-first")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := f.Service.Sources.SubmitImport(ctx, f.Admin, preview.BatchID, []channel.ImportSelection{sel}, "import-first")
	if err != nil || repeated.JobID != change.JobID {
		t.Fatal("import request replayed", err)
	}
	repo := jobs.Repository{DB: f.DB}
	lease, err := repo.Claim(ctx, "source.import")
	if err != nil {
		t.Fatal(err)
	}
	// Expiry after submission must not interrupt accepted rows.
	if _, err = f.DB.Pool.Exec(ctx, "UPDATE source_import_batches SET created_at=clock_timestamp()-interval '31 minutes',expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1", preview.BatchID); err != nil {
		t.Fatal(err)
	}
	done, err := f.Service.Sources.AdvanceImport(ctx, lease)
	if err != nil || !done {
		t.Fatal("rename-only import did not finish", done, err)
	}
	progress, err := f.Service.Sources.GetImport(ctx, f.Admin, preview.BatchID)
	if err != nil || progress.State != "succeeded" || progress.Items[0].State != "skipped" {
		t.Fatal(progress, err)
	}
	var name string
	var current id.ID
	var generation int64
	if err = f.DB.Pool.QueryRow(ctx, "SELECT channel_name,current_revision_id,source_generation FROM channels WHERE id=$1", f.Channel).Scan(&name, &current, &generation); err != nil || name != "Imported name" || current != f.Revision {
		t.Fatal("rename replaced source", err)
	}
	if _, err = f.Service.Sources.TestImport(ctx, f.Admin, preview.BatchID, []int{1}, "expired-test"); err == nil {
		t.Fatal("expired import admitted test")
	}
	if _, err = f.Service.Sources.RetryImport(ctx, f.Admin, preview.BatchID, []channel.ImportSelection{sel}, "expired-retry"); err == nil {
		t.Fatal("expired import admitted retry")
	}
}

func TestImportRowsConflictInvalidAndCancelRemainIndependent(t *testing.T) {
	f := importFixture(t)
	ctx := context.Background()
	preview, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "csv", strings.NewReader("channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Changed,192.168.33.20,554,admin,new,/main,\n2,Invalid,10.99.0.1,554,admin,new,/main,\n3,Conflict,192.168.33.20,554,admin,new,/main,\n"))
	if err != nil {
		t.Fatal(err)
	}
	selections := []channel.ImportSelection{}
	none := "none"
	for _, item := range preview.Items {
		selections = append(selections, channel.ImportSelection{Row: item.Row, ChannelID: *item.ChannelID, ExpectedVersion: *item.ExpectedVersion, IdentityIntent: "replace", PasswordAction: "replace", FirstRecordingMode: &none})
	}
	if _, err = f.DB.Pool.Exec(ctx, "UPDATE channels SET version=version+1 WHERE id=$1", selections[2].ChannelID); err != nil {
		t.Fatal(err)
	}
	_, err = f.Service.Sources.SubmitImport(ctx, f.Admin, preview.BatchID, selections, "three-rows")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := (jobs.Repository{DB: f.DB}).Claim(ctx, "source.import")
	if err != nil {
		t.Fatal(err)
	}
	done, err := f.Service.Sources.AdvanceImport(ctx, lease)
	if err != nil || done {
		t.Fatal("one valid pending row was not kept", done, err)
	}
	progress, err := f.Service.Sources.GetImport(ctx, f.Admin, preview.BatchID)
	if err != nil || progress.Items[0].State != "testing" || progress.Items[1].State != "failed" || progress.Items[2].State != "failed" {
		t.Fatal(progress, err)
	}
	if err = f.Service.Sources.CancelImport(ctx, f.Admin, preview.BatchID); err != nil {
		t.Fatal(err)
	}
	// A begun test owns a durable receipt and is not falsely marked cancelled.
	progress, err = f.Service.Sources.GetImport(ctx, f.Admin, preview.BatchID)
	if err != nil || progress.Items[0].State != "testing" {
		t.Fatal("cancel discarded begun row", progress, err)
	}
}

func TestImportRetryKeepsOldReceiptAndSuccessfulRows(t *testing.T) {
	f := importFixture(t)
	ctx := context.Background()
	preview, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "csv", strings.NewReader("channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Changed,192.168.33.20,554,,,/main,\n"))
	if err != nil {
		t.Fatal(err)
	}
	original := channel.ImportSelection{Row: 1, ChannelID: f.Channel, ExpectedVersion: *preview.Items[0].ExpectedVersion, IdentityIntent: "modify", PasswordAction: "clear"}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET version=version+1 WHERE id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	first, err := f.Service.Sources.SubmitImport(ctx, f.Admin, preview.BatchID, []channel.ImportSelection{original}, "import-conflict")
	if err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: f.DB}
	lease, err := repo.Claim(ctx, "source.import")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, lease, f.Service.ExecuteImport); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := f.DB.Pool.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1", f.Channel).Scan(&version); err != nil {
		t.Fatal(err)
	}
	latest := original
	latest.ExpectedVersion = version
	retry, err := f.Service.Sources.RetryImport(ctx, f.Admin, preview.BatchID, []channel.ImportSelection{latest}, "import-retry")
	if err != nil || retry.JobID == first.JobID {
		t.Fatal("failed row was not independently retried", err)
	}
	old, err := f.Service.Sources.SubmitImport(ctx, f.Admin, preview.BatchID, []channel.ImportSelection{original}, "import-conflict")
	if err != nil || old.JobID != first.JobID {
		t.Fatal("old key lost durable receipt after retry", old, err)
	}
	lease, err = repo.Claim(ctx, "source.import")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, lease, f.Service.ExecuteImport); err != nil {
		t.Fatal(err)
	}
	progress, err := f.Service.Sources.GetImport(ctx, f.Admin, preview.BatchID)
	if err != nil || progress.State != "succeeded" || progress.Items[0].State != "skipped" {
		t.Fatal(progress, err)
	}
	if _, err = f.Service.Sources.RetryImport(ctx, f.Admin, preview.BatchID, []channel.ImportSelection{latest}, "success-replay"); err == nil {
		t.Fatal("successful row replayable")
	}
}

func TestImportParentRecoversAfterLastAttemptAndFencesOldOwner(t *testing.T) {
	f := importFixture(t)
	ctx := context.Background()
	preview, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "csv", strings.NewReader("channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Changed,192.168.33.20,554,,,/main,\n"))
	if err != nil {
		t.Fatal(err)
	}
	sel := channel.ImportSelection{Row: 1, ChannelID: f.Channel, ExpectedVersion: *preview.Items[0].ExpectedVersion, IdentityIntent: "modify", PasswordAction: "clear"}
	change, err := f.Service.Sources.SubmitImport(ctx, f.Admin, preview.BatchID, []channel.ImportSelection{sel}, "resume-import")
	if err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: f.DB}
	old, err := repo.Claim(ctx, "source.import")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.DB.Pool.Exec(ctx, "UPDATE jobs SET attempt=max_attempts,lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", change.JobID); err != nil {
		t.Fatal(err)
	}
	current, err := repo.Claim(ctx, "source.import")
	if err != nil || current.ID != change.JobID {
		t.Fatal("durable parent lost after exhaustion", err)
	}
	if _, err = f.Service.Sources.AdvanceImport(ctx, old); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatal("old owner advanced batch", err)
	}
	if done, err := f.Service.Sources.AdvanceImport(ctx, current); err != nil || !done {
		t.Fatal(done, err)
	}
}

func TestImportRevocationBeforeExecutionDoesNotCreateSource(t *testing.T) {
	f := importFixture(t)
	ctx := context.Background()
	preview, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "csv", strings.NewReader("channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Changed,192.168.33.21,554,,,/main,\n"))
	if err != nil {
		t.Fatal(err)
	}
	sel := channel.ImportSelection{Row: 1, ChannelID: f.Channel, ExpectedVersion: *preview.Items[0].ExpectedVersion, IdentityIntent: "modify", PasswordAction: "clear"}
	_, err = f.Service.Sources.SubmitImport(ctx, f.Admin, preview.BatchID, []channel.ImportSelection{sel}, "revoke-import")
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err = f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM source_revisions").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = f.DB.Pool.Exec(ctx, "UPDATE users SET auth_version=auth_version+1 WHERE id=$1", f.Admin.UserID); err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: f.DB}
	lease, err := repo.Claim(ctx, "source.import")
	if err != nil {
		t.Fatal(err)
	}
	if err = jobs.Execute(ctx, repo, lease, f.Service.ExecuteImport); err != nil {
		t.Fatal(err)
	}
	var after int
	var state, code string
	if err = f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM source_revisions").Scan(&after); err != nil || after != before {
		t.Fatal("revoked import created source", err)
	}
	if err = f.DB.Pool.QueryRow(ctx, "SELECT state,error_code FROM source_import_items WHERE batch_id=$1", preview.BatchID).Scan(&state, &code); err != nil || state != "failed" || code != "authorization_revoked" {
		t.Fatal(state, code, err)
	}
}

func TestImportActualChildJobsApplyWithoutOverwritingPolicy(t *testing.T) {
	f := importFixture(t)
	ctx := context.Background()
	media := newControlledMedia()
	starts := 0
	media.AfterStart = func() { starts++ }
	f.Service.Media = media
	f.Service.Probe = controlledProbe{media: media, file: f.Service.Probe}
	f.Service.ProbeToken = "isolated-import-probe"
	network, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	f.Service.FreshNetwork = func(context.Context) (channel.NetworkPolicy, error) { return network, nil }
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET storage_pool_id=$2 WHERE id=$1", f.Channel, f.Pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='none',event_recording=true WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	csv := "channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Imported,192.168.33.21,554,,,/main,/sub\n"
	preview, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "csv", strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	test, err := f.Service.Sources.TestImport(ctx, f.Admin, preview.BatchID, []int{1}, "import-preview-test")
	if err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: f.DB}
	parent, err := repo.Claim(ctx, "source.import")
	if err != nil {
		t.Fatal(err)
	}
	if done, err := f.Service.Sources.AdvanceImport(ctx, parent); err != nil || done {
		t.Fatal(done, err)
	}
	replay, err := f.Service.Sources.TestImport(ctx, f.Admin, preview.BatchID, []int{1}, "import-preview-test")
	if err != nil || replay.JobID != test.JobID {
		t.Fatal("test replay changed after draft creation", err)
	}
	child, err := repo.Claim(ctx, "source.test")
	if err != nil {
		t.Fatal(err)
	}
	if err = jobs.Execute(ctx, repo, child, f.Service.ExecuteSourceTest); err != nil {
		t.Fatal(err)
	}
	if err = jobs.Execute(ctx, repo, parent, f.Service.ExecuteImport); err != nil {
		t.Fatal(err)
	}
	var current id.ID
	if err = f.DB.Pool.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", f.Channel).Scan(&current); err != nil || current != f.Revision {
		t.Fatal("preview test activated source", err)
	}
	progress, err := f.Service.Sources.GetImport(ctx, f.Admin, preview.BatchID)
	if err != nil || progress.Items[0].State != "tested" {
		t.Fatal(progress, err)
	}
	selection := channel.ImportSelection{Row: 1, ChannelID: f.Channel, ExpectedVersion: *progress.Items[0].ExpectedVersion, IdentityIntent: "modify", PasswordAction: "clear", ImportName: true}
	if _, err = f.Service.Sources.SubmitImport(ctx, f.Admin, preview.BatchID, []channel.ImportSelection{selection}, "import-apply-children"); err != nil {
		t.Fatal(err)
	}
	parent, err = repo.Claim(ctx, "source.import")
	if err != nil {
		t.Fatal(err)
	}
	if done, err := f.Service.Sources.AdvanceImport(ctx, parent); err != nil || done {
		t.Fatal(done, err)
	}
	child, err = repo.Claim(ctx, "source.test")
	if err != nil {
		t.Fatal(err)
	}
	if err = jobs.Execute(ctx, repo, child, f.Service.ExecuteSourceTest); err != nil {
		t.Fatal(err)
	}
	if done, err := f.Service.Sources.AdvanceImport(ctx, parent); err != nil || done {
		t.Fatal(done, err)
	}
	child, err = repo.Claim(ctx, "source.apply")
	if err != nil {
		t.Fatal(err)
	}
	if err = jobs.Execute(ctx, repo, child, f.Service.ExecuteSourceChange); err != nil {
		t.Fatal(err)
	}
	if err = jobs.Execute(ctx, repo, parent, f.Service.ExecuteImport); err != nil {
		t.Fatal(err)
	}
	progress, err = f.Service.Sources.GetImport(ctx, f.Admin, preview.BatchID)
	if err != nil || progress.State != "succeeded" || progress.Items[0].State != "succeeded" {
		t.Fatal(progress, err)
	}
	var mode, name string
	var event bool
	var pool id.ID
	if err = f.DB.Pool.QueryRow(ctx, "SELECT p.mode,p.event_recording,c.storage_pool_id,c.channel_name,c.current_revision_id FROM recording_policies p JOIN channels c ON c.id=p.channel_id WHERE c.id=$1", f.Channel).Scan(&mode, &event, &pool, &name, &current); err != nil || mode != "none" || !event || pool != f.Pool.ID || name != "Imported" || current == f.Revision {
		t.Fatal("import overwrote policy/pool/history", err, mode, event, pool, name)
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	if starts != 0 {
		t.Fatal("recording-off import implicitly recorded")
	}
}

func TestImportMappingCanSwapTargetsWithoutDuplicate(t *testing.T) {
	f := importFixture(t)
	ctx := context.Background()
	preview, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "csv", strings.NewReader("channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,First,192.168.33.20,554,,,/main,\n2,Second,192.168.33.21,554,,,/main,\n"))
	if err != nil {
		t.Fatal(err)
	}
	none := "none"
	items := []channel.ImportSelection{{Row: 1, ChannelID: *preview.Items[1].ChannelID, ExpectedVersion: *preview.Items[1].ExpectedVersion, IdentityIntent: "replace", PasswordAction: "clear", FirstRecordingMode: &none}, {Row: 2, ChannelID: *preview.Items[0].ChannelID, ExpectedVersion: *preview.Items[0].ExpectedVersion, IdentityIntent: "modify", PasswordAction: "clear"}}
	if _, err = f.Service.Sources.SubmitImport(ctx, f.Admin, preview.BatchID, items, "swap-import"); err != nil {
		t.Fatal("explicit unique mapping rejected", err)
	}
}

func TestExpiredImportPurgesEncryptedDraftButPreservesAcceptedWork(t *testing.T) {
	f := importFixture(t)
	ctx := context.Background()
	csv := "channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Front,192.168.33.21,554,,private-import-expiry,/main,\n"
	idle, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "csv", strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "csv", strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	selection := channel.ImportSelection{Row: 1, ChannelID: f.Channel, ExpectedVersion: *accepted.Items[0].ExpectedVersion, IdentityIntent: "modify", PasswordAction: "replace"}
	if _, err = f.Service.Sources.SubmitImport(ctx, f.Admin, accepted.BatchID, []channel.ImportSelection{selection}, "retain-accepted-draft"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.DB.Pool.Exec(ctx, "UPDATE source_import_batches SET created_at=clock_timestamp()-interval '31 minutes',expires_at=clock_timestamp()-interval '1 minute' WHERE id=ANY($1)", []id.ID{idle.BatchID, accepted.BatchID}); err != nil {
		t.Fatal(err)
	}
	if err = f.Service.Sources.CleanupExpiredImports(ctx); err != nil {
		t.Fatal(err)
	}
	for _, batch := range []id.ID{idle.BatchID, accepted.BatchID} {
		var exists bool
		if err = f.DB.Pool.QueryRow(ctx, "SELECT draft_ciphertext IS NOT NULL FROM source_import_items WHERE batch_id=$1", batch).Scan(&exists); err != nil || exists != (batch == accepted.BatchID) {
			t.Fatal("expired draft lifecycle incorrect", batch, exists, err)
		}
	}
	if _, err = f.Service.Sources.GetImport(ctx, f.Admin, idle.BatchID); err != nil {
		t.Fatal("purged summary unavailable", err)
	}
	if _, err = f.Service.Sources.TestImport(ctx, f.Admin, idle.BatchID, []int{1}, "test-purged-expired"); err == nil {
		t.Fatal("purged expiry allowed a new test")
	}
}

func TestImportPreviewProvidesSafePasswordIntent(t *testing.T) {
	f := importFixture(t)
	for _, tc := range []struct{ format, input, action string }{
		{"csv", "channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Front,192.168.33.24,554,admin,,/main,/sub\n", "clear"},
		{"csv", "channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Front,192.168.33.24,554,admin,isolated-intent-secret,/main,/sub\n", "replace"},
		{"json", `{"format_version":1,"channels":[{"channel_no":1,"channel_name":"Front","ip":"192.168.33.24","rtsp_port":554,"main_path":"/main","sub_path":"/sub"}]}`, "keep"},
	} {
		preview, err := f.Service.Sources.PreviewImport(context.Background(), f.Admin, tc.format, strings.NewReader(tc.input))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(preview.Items[0])
		var item map[string]any
		_ = json.Unmarshal(raw, &item)
		if item["password_action"] != tc.action {
			t.Fatalf("safe preview intent = %v, want %s", item["password_action"], tc.action)
		}
		progress, err := f.Service.Sources.GetImport(context.Background(), f.Admin, preview.BatchID)
		if err != nil || progress.Items[0].PasswordAction != tc.action {
			t.Fatal("progress lost parsed password intent", err)
		}
		progressJSON, _ := json.Marshal(progress)
		var metadata map[string]any
		_ = json.Unmarshal(progressJSON, &metadata)
		if metadata["expires_at"] == nil {
			t.Fatal("resumed preview has no server expiry")
		}
		if bytes.Contains(raw, []byte("isolated-intent-secret")) {
			t.Fatal("preview contains plaintext")
		}
	}
}

func TestImportClearedChannelRetainsFirstApplyHistory(t *testing.T) {
	f, _, test, revision := testedSource(t)
	ctx := context.Background()
	if _, err := f.Service.Sources.RequestApply(ctx, f.Admin, f.Channel, channel.SourceApplyInput{RevisionID: revision, TestID: *test.TestID, ExpectedVersion: 3}, "import-history-apply"); err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.apply")
	var version int64
	if err := f.DB.Pool.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1", f.Channel).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.Sources.RequestClear(ctx, f.Admin, f.Channel, version, "import-history-clear"); err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.clear")
	if err := f.DB.Pool.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1", f.Channel).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, version, "none", "import-cleared-policy"); err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.policy_apply")
	historical, err := f.Service.Publish(ctx, f.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	input := "channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Front,192.168.33.24,554,admin,,/main,\n2,Second,192.168.33.25,554,admin,,/main,\n"
	preview, err := f.Service.Sources.PreviewImport(ctx, f.Admin, "csv", strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(preview)
	var safe struct {
		Items []struct {
			Requires bool `json:"requires_initial_recording_mode"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &safe); err != nil {
		t.Fatal(err)
	}
	if safe.Items[0].Requires || !safe.Items[1].Requires {
		t.Fatal("cleared and never-configured channels treated alike", string(raw))
	}
	progress, err := f.Service.Sources.GetImport(ctx, f.Admin, preview.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := json.Marshal(progress)
	if !bytes.Contains(saved, []byte(`"requires_initial_recording_mode":true`)) {
		t.Fatal("progress lost first-configuration metadata")
	}

	selection := channel.ImportSelection{Row: 1, ChannelID: f.Channel, ExpectedVersion: *preview.Items[0].ExpectedVersion, IdentityIntent: "replace", PasswordAction: "clear"}
	if _, err := f.Service.Sources.SubmitImport(ctx, f.Admin, preview.BatchID, []channel.ImportSelection{selection}, "cleared-import-apply"); err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: f.DB}
	parent, err := repo.Claim(ctx, "source.import")
	if err != nil {
		t.Fatal(err)
	}
	if done, err := f.Service.Sources.AdvanceImport(ctx, parent); err != nil || done {
		t.Fatal("import did not schedule actual test", done, err)
	}
	child, err := repo.Claim(ctx, "source.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, child, f.Service.ExecuteSourceTest); err != nil {
		t.Fatal(err)
	}
	if done, err := f.Service.Sources.AdvanceImport(ctx, parent); err != nil || done {
		t.Fatal("import did not schedule apply", done, err)
	}
	executeChange(t, f, "source.apply")
	if err := jobs.Execute(ctx, repo, parent, f.Service.ExecuteImport); err != nil {
		t.Fatal(err)
	}
	progress, err = f.Service.Sources.GetImport(ctx, f.Admin, preview.BatchID)
	if err != nil || progress.Items[0].State != "succeeded" {
		t.Fatal("cleared target import failed", progress, err)
	}
	var mode string
	var event bool
	var pool id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT p.mode,p.event_recording,c.storage_pool_id FROM recording_policies p JOIN channels c ON c.id=p.channel_id WHERE c.id=$1", f.Channel).Scan(&mode, &event, &pool); err != nil || mode != "none" || !event || pool != f.Pool.ID {
		t.Fatal("import reset existing policy", mode, event, pool, err)
	}
	var retained bool
	if err := f.DB.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM recording_segments WHERE id=$1 AND state='ready' AND source_revision_id=$2 AND run_id=$3)", historical.ID, f.Revision, f.Run).Scan(&retained); err != nil || !retained {
		t.Fatal("cleared-channel import lost historical file", retained, err)
	}
}
