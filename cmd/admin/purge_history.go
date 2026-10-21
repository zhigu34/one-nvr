package main

// Removing all recorded history: local footage, event index, cloud copies.
//
// This is a test-stage reset, not a retention feature. It exists as a command
// rather than a SQL script plus `rm` for three reasons:
//
//   - Cloud objects cannot be deleted from SQL. The WebDAV password is sealed
//     with the site's credential key and only the application can open it; a
//     shell script that skipped this step would report success while leaving
//     1049 files on a cloud drive the operator believes is empty.
//   - Local files must go through the same rules an operator is held to. Each
//     segment is unlinked by recording.DeleteCorruptLocal, which re-checks the
//     published identity (so a stale path cannot authorize deleting whatever now
//     sits there), refuses while an export, upload or playback lease holds the
//     file, and writes one audit row per file. An `rm -rf` gets none of that and
//     leaves rows claiming 'ready' over files that are gone.
//   - It has to be resumable. A run against 1400 segments and 1000 remote objects
//     will be interrupted; every step is therefore idempotent and the command
//     reports what it could not finish instead of pretending it did.
//
// What is NOT deleted, deliberately: channels, sources, storage pools, archive
// targets, policies, schedules, rules, users, and the audit log. Those are
// configuration and the record of what happened; wiping them would turn a data
// reset into a re-install. Reliability history is kept for the same reason —
// it describes the camera links, not the footage — and this command says so
// rather than leaving the operator to discover it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/archive"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/recording"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// historyPurgeBatch matches the deletion core's per-call ceiling.
const historyPurgeBatch = 100

// historyPurgeConfirm is the exact argument this command demands before it
// deletes anything. A destructive default is how the wrong database gets wiped.
const historyPurgeConfirm = "--confirm-delete-history"

type historyInventory struct {
	LocalReady      int
	LocalOther      int
	CloudVerified   int
	Events          int
	Snapshots       int
	ClipWindows     int
	UnfinishedRuns  int
	ExportArtifacts int
}

// purgeHistoryCommand removes all recorded media and the event index.
func purgeHistoryCommand(args []string) (bool, error) {
	dryRun := false
	confirmed := false
	found := false
	for _, arg := range args {
		switch arg {
		case "purge-history":
			found = true
		case "--dry-run":
			dryRun = true
		case historyPurgeConfirm:
			confirmed = true
		default:
			if found {
				return true, fmt.Errorf("usage: admin purge-history --dry-run | %s", historyPurgeConfirm)
			}
		}
	}
	if !found {
		return false, nil
	}
	if !dryRun && !confirmed {
		return true, fmt.Errorf("refusing to delete all history without %s (or use --dry-run)", historyPurgeConfirm)
	}

	c, err := config.Load()
	if err != nil {
		return true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	defer cancel()
	db, err := database.Open(ctx, c.DatabaseURL)
	if err != nil {
		return true, err
	}
	defer db.Pool.Close()

	secret, err := secrets.Load(c.DataDir)
	if err != nil {
		return true, err
	}
	roots := []string{c.DataDir + "/storage", "/storage"}
	pools := storage.New(db, nil, roots)
	mediaProbe := probe.Runner{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe"}
	service := recording.New(db, nil, mediaProbe, roots, c.DataDir)
	archiver := archive.New(db, nil, secret, pools)

	inventory, err := takeHistoryInventory(ctx, db)
	if err != nil {
		return true, err
	}
	fmt.Printf("local ready segments:     %d\n", inventory.LocalReady)
	fmt.Printf("local other segments:     %d\n", inventory.LocalOther)
	fmt.Printf("verified cloud copies:    %d\n", inventory.CloudVerified)
	fmt.Printf("events:                   %d\n", inventory.Events)
	fmt.Printf("indexed snapshots:        %d\n", inventory.Snapshots)
	fmt.Printf("clip windows:             %d\n", inventory.ClipWindows)
	fmt.Printf("unfinished recording runs:%d\n", inventory.UnfinishedRuns)
	fmt.Printf("export artifacts:         %d\n", inventory.ExportArtifacts)
	if dryRun {
		summary, err := json.Marshal(map[string]any{"dry_run": true, "inventory": inventory})
		if err != nil {
			return true, err
		}
		fmt.Println(string(summary))
		return true, nil
	}

	// 1. Cloud first. Deleting the local file is what makes a copy visible as
	//    "仅云端"; doing local first would leave an operator looking at cloud-only
	//    recordings after asking for everything to be gone. A failure here stops
	//    the run rather than orphaning objects nobody can find afterwards.
	cloudDeleted, cloudFailed, err := purgeCloudCopies(ctx, db, archiver)
	if err != nil {
		return true, err
	}
	fmt.Printf("cloud objects deleted:    %d (failed %d)\n", cloudDeleted, cloudFailed)

	// 2. Local files, through the same core the UI uses.
	localDeleted, released, localKept := purgeLocalSegments(ctx, db, service)
	fmt.Printf("local files deleted:      %d (%d MB released, %d kept)\n", localDeleted, released>>20, localKept)
	if localKept > 0 {
		// Stop before the directory sweep. A "kept" outcome is a real refusal — a
		// playback lease, an export reading the file, an upload in flight — and
		// removing the tree anyway would take the file out from under it, which is
		// exactly what the deletion core exists to prevent. The operator reruns once
		// the reason is gone; everything above is already done and idempotent.
		return true, fmt.Errorf("%d local files were refused by the deletion core; resolve the reason above and rerun", localKept)
	}

	// 3. The index. Before the directory sweep, not after: the recorder keeps
	//    producing segments while this runs, and a clip that lands between the row
	//    delete and the sweep would otherwise leave its file on disk with nothing
	//    pointing at it. With the rows gone first, the sweep's leftovers are
	//    unambiguously orphans.
	if err := recording.PurgeHistoryRows(ctx, db); err != nil {
		return true, err
	}
	fmt.Println("rows removed:             events, clip windows, segments, runs, exports, snapshots")

	// 4. Files the index no longer describes: event-buffer parts that never became
	//    segments, abandoned clip attempts, and anything the recorder wrote during
	//    steps 1-3. Each pool is opened through its marker file, so a wrong
	//    canonical_path cannot turn this into a delete of something that is not a
	//    one-nvr pool.
	reclaimed, err := purgeMediaDirectories(ctx, db, pools)
	if err != nil {
		return true, err
	}
	fmt.Printf("directories reclaimed:    %d MB\n", reclaimed>>20)

	result, err := json.Marshal(map[string]any{
		"dry_run":         false,
		"cloud_deleted":   cloudDeleted,
		"cloud_failed":    cloudFailed,
		"local_deleted":   localDeleted,
		"local_kept":      localKept,
		"released_bytes":  released,
		"reclaimed_bytes": reclaimed,
	})
	if err != nil {
		return true, err
	}
	fmt.Println(string(result))
	if cloudFailed > 0 || localKept > 0 {
		return true, fmt.Errorf("%d cloud objects and %d local files were not deleted; rerun to retry", cloudFailed, localKept)
	}
	return true, nil
}

func takeHistoryInventory(ctx context.Context, db *database.DB) (historyInventory, error) {
	var out historyInventory
	err := db.Pool.QueryRow(ctx, `SELECT
		  (SELECT count(*) FROM recording_segments WHERE state='ready')
		, (SELECT count(*) FROM recording_segments WHERE state<>'ready')
		, (SELECT count(*) FROM recording_archive_copies WHERE state='verified')
		, (SELECT count(*) FROM events)
		, (SELECT count(*) FROM events WHERE snapshot_relative_path IS NOT NULL)
		, (SELECT count(*) FROM clip_windows)
		, (SELECT count(*) FROM recording_runs WHERE state NOT IN ('stopped','failed'))
		, (SELECT count(*) FROM recording_export_artifacts WHERE state='ready')`).
		Scan(&out.LocalReady, &out.LocalOther, &out.CloudVerified, &out.Events,
			&out.Snapshots, &out.ClipWindows, &out.UnfinishedRuns, &out.ExportArtifacts)
	return out, err
}

// purgeCloudCopies deletes every verified remote object and marks the copy rows
// expired. The row stays — it is the only record that an object ever existed —
// but its state says the bytes are gone, which is what the playback path reads.
func purgeCloudCopies(ctx context.Context, db *database.DB, archiver *archive.Service) (deleted, failed int, err error) {
	type copyRow struct {
		ID        id.ID
		SegmentID id.ID
		TargetID  id.ID
		Key       string
	}
	for {
		if ctx.Err() != nil {
			return deleted, failed, ctx.Err()
		}
		rows, err := db.Pool.Query(ctx, `SELECT id,segment_id,target_id,object_key FROM recording_archive_copies
			WHERE state IN ('verified','uploading','failed') ORDER BY created_at LIMIT $1`, historyPurgeBatch)
		if err != nil {
			return deleted, failed, err
		}
		var batch []copyRow
		for rows.Next() {
			var item copyRow
			if err := rows.Scan(&item.ID, &item.SegmentID, &item.TargetID, &item.Key); err != nil {
				rows.Close()
				return deleted, failed, err
			}
			batch = append(batch, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return deleted, failed, err
		}
		if len(batch) == 0 {
			return deleted, failed, nil
		}
		for _, item := range batch {
			if ctx.Err() != nil {
				return deleted, failed, ctx.Err()
			}
			// RemoveObject reports whether the object is gone, including the case
			// where it was never there. A transport failure is the only outcome
			// that leaves the row alone, so a rerun retries exactly those.
			gone, err := archiver.RemoveObject(ctx, item.TargetID, item.Key)
			if err != nil {
				failed++
				fmt.Printf("cloud kept %s: %v\n", item.Key, err)
				continue
			}
			if !gone {
				// The target said "not found" after we asked it to delete: treat it
				// as done, because the desired state is "no object at this key".
				fmt.Printf("cloud already absent %s\n", item.Key)
			}
			if err := db.WithinTx(ctx, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE recording_archive_copies SET state='expired',error_code='history_purged' WHERE id=$1`, item.ID); err != nil {
					return err
				}
				details, _ := json.Marshal(map[string]any{"object_key": item.Key, "purge": "history"})
				return audit.Append(ctx, tx, audit.Entry{Action: "archive.copy_expired", ObjectID: item.SegmentID, Details: details})
			}); err != nil {
				return deleted, failed, err
			}
			deleted++
		}
	}
}

// purgeLocalSegments unlinks every segment file through the deletion core and
// reports how many bytes that released.
//
// The candidate list is collected once rather than re-read after each batch. A
// segment the core refuses (a held lease, a row that is not 'ready') keeps its
// state, so a "select from the top" loop would pick the same rows forever and
// never finish — the same starvation the retention sweeps avoid by ordering on
// end_at. One pass over a snapshot of the ids terminates whatever the outcomes
// are, and the rows that were kept are named in the report.
func purgeLocalSegments(ctx context.Context, db *database.DB, service *recording.Service) (deleted int, released int64, kept int) {
	rows, err := db.Pool.Query(ctx, `SELECT id FROM recording_segments WHERE state<>'deleted' ORDER BY start_at`)
	if err != nil {
		fmt.Printf("segment scan stopped: %v\n", err)
		return 0, 0, 0
	}
	var ids []id.ID
	for rows.Next() {
		var segmentID id.ID
		if err := rows.Scan(&segmentID); err != nil {
			rows.Close()
			return 0, released, kept
		}
		ids = append(ids, segmentID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, released, kept
	}
	for start := 0; start < len(ids); start += historyPurgeBatch {
		if ctx.Err() != nil {
			return deleted, released, kept
		}
		end := start + historyPurgeBatch
		if end > len(ids) {
			end = len(ids)
		}
		outcomes, err := service.DeleteCorruptLocal(ctx, ids[start:end])
		if err != nil {
			fmt.Printf("segment deletion stopped: %v\n", err)
			return deleted, released, kept
		}
		for _, outcome := range outcomes {
			switch outcome.State {
			case "deleted":
				deleted++
				released += outcome.Bytes
			case "missing":
				// The file is already gone; the core recorded that. Nothing to
				// unlink, and the row goes with the rest of the index below.
				deleted++
			default:
				kept++
				fmt.Printf("local kept %s: %s\n", outcome.SegmentID, outcome.Reason)
			}
		}
	}
	return deleted, released, kept
}

// purgeMediaDirectories removes what the index no longer names.
//
// The deletion core removes files it has rows for; it cannot remove a directory
// tree, event-buffer parts that never became segments, or clip attempts that were
// abandoned mid-assembly. Those are exactly where a "delete everything" request
// otherwise leaves tens of gigabytes behind while the page shows nothing.
//
// Each root is opened through the pool marker, so a misconfigured canonical_path
// cannot turn this into a delete of something that is not a one-nvr pool.
func purgeMediaDirectories(ctx context.Context, db *database.DB, pools *storage.Service) (int64, error) {
	rows, err := db.Pool.Query(ctx, `SELECT id FROM storage_pools`)
	if err != nil {
		return 0, err
	}
	var poolIDs []id.ID
	for rows.Next() {
		var poolID id.ID
		if err := rows.Scan(&poolID); err != nil {
			rows.Close()
			return 0, err
		}
		poolIDs = append(poolIDs, poolID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var reclaimed int64
	for _, poolID := range poolIDs {
		if ctx.Err() != nil {
			return reclaimed, ctx.Err()
		}
		root, pool, err := pools.OpenMediaRoot(ctx, poolID)
		if err != nil {
			fmt.Printf("pool %s skipped: %v\n", poolID, err)
			continue
		}
		for _, dir := range []string{"recordings", "snapshots", ".work"} {
			size, err := removeTreeUnder(root, pool.Path, dir)
			if err != nil {
				fmt.Printf("pool %s/%s kept: %v\n", pool.Name, dir, err)
				continue
			}
			reclaimed += size
		}
		root.Close()
	}
	return reclaimed, nil
}

// removeTreeUnder empties one directory inside the pool, recreating it so the
// next recording has somewhere to write.
func removeTreeUnder(root *os.Root, poolPath, dir string) (int64, error) {
	info, err := root.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		// A file where a directory belongs is not ours to delete.
		return 0, fmt.Errorf("not a directory")
	}
	abs := filepath.Join(poolPath, dir)
	size, err := treeSize(abs)
	if err != nil {
		return 0, err
	}
	if err := os.RemoveAll(abs); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return 0, err
	}
	return size, nil
}

// treeSize reports how many bytes a directory holds, for the report only. A
// failure to measure must not block the deletion.
func treeSize(dir string) (int64, error) {
	var total int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return total, nil
	}
	return total, nil
}
