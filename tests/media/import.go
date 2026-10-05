package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/recording"
)

func importAcceptance() error { return sourceLifecycleAcceptance("import") }
func recordingImportScenario(ctx context.Context, db *database.DB, s *recording.Service, p auth.Principal, ids poolFixtureIDs, oldRun id.ID, execute func(string, jobs.Handler) error) error {
	csv := fmt.Sprintf("channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n1,Imported main,%s,554,,,/one_nvr/%s,\n2,Unavailable main,%s,554,,,/missing-import-fixture,\n3,Conflict main,%s,554,,,/one_nvr/%s,\n", ids.Config.IP, streams[2], ids.Config.IP, ids.Config.IP, streams[0])
	preview, err := s.Sources.PreviewImport(ctx, p, "csv", strings.NewReader(csv))
	if err != nil {
		return err
	}
	none := "none"
	selections := []channel.ImportSelection{}
	for _, row := range preview.Items {
		intent := "replace"
		var first *string = &none
		if row.Row == 1 {
			intent = "modify"
			first = nil
		}
		selections = append(selections, channel.ImportSelection{Row: row.Row, ChannelID: *row.ChannelID, ExpectedVersion: *row.ExpectedVersion, IdentityIntent: intent, PasswordAction: "clear", ImportName: true, FirstRecordingMode: first})
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE recording_policies SET event_recording=true WHERE channel_id=$1", ids.Channel); err != nil {
		return err
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE channels SET version=version+1 WHERE id=$1", selections[2].ChannelID); err != nil {
		return err
	}
	change, err := s.Sources.SubmitImport(ctx, p, preview.BatchID, selections, "actual-import-mixed")
	if err != nil {
		return err
	}
	repo := jobs.Repository{DB: db}
	parent, err := repo.Claim(ctx, "source.import")
	if err != nil {
		return err
	}
	if done, err := s.Sources.AdvanceImport(ctx, parent); err != nil || done {
		return fmt.Errorf("actual import preparation failed: %w", err)
	}
	if err := execute("source.test", s.ExecuteSourceTest); err != nil {
		return err
	}
	if err := repo.Renew(ctx, parent); err != nil {
		return err
	}
	if err := s.CheckPool(ctx, ids.Pool); err != nil {
		return err
	}
	if _, err := s.Sources.AdvanceImport(ctx, parent); err != nil {
		return err
	}
	if err := execute("source.apply", s.ExecuteSourceChange); err != nil {
		return err
	}
	if err := repo.Renew(ctx, parent); err != nil {
		return err
	}
	if _, err := s.Sources.AdvanceImport(ctx, parent); err != nil {
		return err
	}
	if err := execute("source.test", s.ExecuteSourceTest); err != nil {
		return err
	}
	if err := repo.Renew(ctx, parent); err != nil {
		return err
	}
	if err := jobs.Execute(ctx, repo, parent, s.ExecuteImport); err != nil {
		return err
	}
	progress, err := s.Sources.GetImport(ctx, p, preview.BatchID)
	if err != nil {
		return err
	}
	if progress.State != "partial" || len(progress.Items) != 3 || progress.Items[0].State != "succeeded" || progress.Items[1].State != "failed" || progress.Items[2].State != "failed" {
		return fmt.Errorf("actual mixed import did not retain independent row outcomes")
	}
	replay, err := s.Sources.SubmitImport(ctx, p, preview.BatchID, selections, "actual-import-mixed")
	if err != nil || replay.JobID != change.JobID {
		return fmt.Errorf("actual import replay did not preserve receipt")
	}
	var mode, name string
	var event bool
	var pool, run, revision id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT p.mode,p.event_recording,c.storage_pool_id,c.channel_name,c.current_revision_id FROM channels c JOIN recording_policies p ON p.channel_id=c.id WHERE c.id=$1", ids.Channel).Scan(&mode, &event, &pool, &name, &revision); err != nil {
		return err
	}
	if mode != "continuous" || !event || pool != ids.Pool || name != "Imported main" || revision == ids.Revision {
		return fmt.Errorf("actual import changed policy/pool or lost replacement")
	}
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM recording_runs WHERE channel_id=$1 AND state='recording'", ids.Channel).Scan(&run); err != nil || run == oldRun {
		return fmt.Errorf("actual import did not verify a fresh continuous run")
	}
	raw, _ := json.MarshalIndent(map[string]bool{"mixed_three_rows_independent": true, "real_source_test_and_switch": true, "current_version_conflict_rejected": true, "recording_policy_pool_event_preserved": true, "original_request_receipt_replayed": true}, "", "  ")
	if err := os.WriteFile("/evidence/import.json", raw, 0644); err != nil {
		return err
	}
	fmt.Println("Actual ZLM three-row import, per-row failure/conflict, preserved policy/pool/history and durable replay PASS")
	return nil
}
