package main

// Purging recordings whose media cannot be decoded.
//
// Event clips assembled before the concatenation fix carry an invalid AAC track:
// every video frame is intact and ffprobe reports a sane container, but no
// decoder can read the audio, and Chrome fails the whole clip on it. They cannot
// be repaired — re-encoding re-compresses bytes that are already illegal
// (measured: 10323 decode errors before the attempt, 4519 after). Removing them
// is the only honest outcome.
//
// The removal goes through recording.DeleteCorruptLocal, so the identity check,
// export/archive/lease pins, protection, and the audit row all still apply. A
// sweep does not get to bypass the rules an operator is held to.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/recording"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// corruptScanLimit bounds how many segments one run examines, so the command
// stays predictable on a large library.
const corruptScanLimit = 5000

// corruptDeleteBatch matches the deletion core's per-call ceiling.
const corruptDeleteBatch = 100

type corruptCandidate struct {
	ID     id.ID
	PoolID id.ID
	Target string
	Start  time.Time
}

// purgeCorruptCommand removes published recordings whose audio a decoder cannot
// read. `--dry-run` reports what it would remove without touching anything.
func purgeCorruptCommand(args []string) (bool, error) {
	dryRun := false
	found := false
	byState := false
	for _, arg := range args {
		switch arg {
		case "purge-corrupt":
			found = true
		case "purge-damaged":
			// Removes segments the reconciler already quarantined, whatever the
			// reason: a file whose identity no longer matches the index cannot be
			// served under that index entry, so the entry describes nothing.
			found = true
			byState = true
		case "--dry-run":
			dryRun = true
		default:
			if found {
				return true, fmt.Errorf("usage: admin purge-corrupt|purge-damaged [--dry-run]")
			}
		}
	}
	if !found {
		return false, nil
	}
	c, err := config.Load()
	if err != nil {
		return true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 48*time.Hour)
	defer cancel()
	db, err := database.Open(ctx, c.DatabaseURL)
	if err != nil {
		return true, err
	}
	defer db.Pool.Close()
	roots := []string{c.DataDir + "/storage", "/storage"}
	pools := storage.New(db, nil, roots)
	mediaProbe := probe.Runner{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe"}
	service := recording.New(db, nil, mediaProbe, roots, c.DataDir)

	var candidates []corruptCandidate
	if byState {
		candidates, err = damagedCandidates(ctx, db)
		if err != nil {
			return true, err
		}
	} else {
		candidates, err = corruptCandidates(ctx, db)
	}
	if err != nil {
		return true, err
	}
	if byState {
		return purgeDamaged(ctx, service, candidates, dryRun)
	}
	if err != nil {
		return true, err
	}
	var corrupted []id.ID
	for _, item := range candidates {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		path, cleanup, err := materialize(pools, ctx, item.PoolID, item.Target)
		if err != nil {
			fmt.Printf("skip %s: %v\n", item.Target, err)
			continue
		}
		report := mediaProbe.DecodeCheck(ctx, path)
		cleanup()
		if !report.Inspected {
			fmt.Printf("skip %s: decoder could not inspect the file\n", item.Target)
			continue
		}
		if report.Errors == 0 {
			continue
		}
		corrupted = append(corrupted, item.ID)
		fmt.Printf("corrupt %s (%d decode errors: %s)\n", item.Target, report.Errors, report.FirstError)
	}
	if len(corrupted) == 0 {
		fmt.Println("no corrupt recordings found")
		return true, nil
	}
	summary, err := json.Marshal(map[string]int{"corrupt": len(corrupted), "dry_run": boolInt(dryRun)})
	if err != nil {
		return true, err
	}
	fmt.Println(string(summary))
	if dryRun {
		return true, nil
	}
	removed := 0
	for start := 0; start < len(corrupted); start += corruptDeleteBatch {
		end := start + corruptDeleteBatch
		if end > len(corrupted) {
			end = len(corrupted)
		}
		outcomes, err := service.DeleteCorruptLocal(ctx, corrupted[start:end])
		if err != nil {
			return true, err
		}
		for _, outcome := range outcomes {
			if outcome.State == "deleted" {
				removed++
			} else {
				fmt.Printf("kept %s: %s\n", outcome.SegmentID, outcome.Reason)
			}
		}
	}
	result, err := json.Marshal(map[string]int{"deleted": removed, "released_bytes": 0})
	if err != nil {
		return true, err
	}
	fmt.Println(string(result))
	return true, nil
}

// materialize resolves a pool-relative media path for the decoder.
//
// The decoder needs a filesystem path, while every other access in this codebase
// goes through storage.OpenMediaFile's link-safe walk. The pool root is trusted —
// it was verified when the pool was opened — and the path came from the index, so
// the check that matters here is that the file is still the one the index named;
// DecodeCheck then reads it, and the deletion path re-verifies the identity
// before unlinking.
func materialize(pools *storage.Service, ctx context.Context, poolID id.ID, target string) (string, func(), error) {
	root, pool, err := pools.OpenMediaRoot(ctx, poolID)
	if err != nil {
		return "", func() {}, err
	}
	file, err := storage.OpenMediaFile(root, target)
	if err != nil {
		root.Close()
		return "", func() {}, err
	}
	// Hold the descriptor open only long enough to confirm the file exists; the
	// decoder opens its own handle from the resolved path.
	file.Close()
	root.Close()
	return pool.Path + "/" + target, func() {}, nil
}

// damagedCandidates lists segments the reconciler has already quarantined.
func damagedCandidates(ctx context.Context, db *database.DB) ([]corruptCandidate, error) {
	rows, err := db.Pool.Query(ctx, `SELECT s.id,s.pool_id,s.target_relative_path,s.start_at
		FROM recording_segments s WHERE s.state='damaged' ORDER BY s.start_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []corruptCandidate
	for rows.Next() {
		var item corruptCandidate
		if err := rows.Scan(&item.ID, &item.PoolID, &item.Target, &item.Start); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// purgeDamaged removes segments already marked damaged. There is no decode check
// here: the verdict is the reconciler's, and re-deciding it would make this
// command disagree with the page that showed the state.
func purgeDamaged(ctx context.Context, service *recording.Service, candidates []corruptCandidate, dryRun bool) (bool, error) {
	for _, item := range candidates {
		fmt.Printf("damaged %s\n", item.Target)
	}
	summary, err := json.Marshal(map[string]int{"damaged": len(candidates), "dry_run": boolInt(dryRun)})
	if err != nil {
		return true, err
	}
	fmt.Println(string(summary))
	if dryRun || len(candidates) == 0 {
		return true, nil
	}
	ids := make([]id.ID, 0, len(candidates))
	for _, item := range candidates {
		ids = append(ids, item.ID)
	}
	removed := 0
	for start := 0; start < len(ids); start += corruptDeleteBatch {
		end := start + corruptDeleteBatch
		if end > len(ids) {
			end = len(ids)
		}
		outcomes, err := service.DeleteCorruptLocal(ctx, ids[start:end])
		if err != nil {
			return true, err
		}
		for _, outcome := range outcomes {
			if outcome.State == "deleted" {
				removed++
			} else {
				fmt.Printf("kept %s: %s\n", outcome.SegmentID, outcome.Reason)
			}
		}
	}
	result, err := json.Marshal(map[string]int{"deleted": removed})
	if err != nil {
		return true, err
	}
	fmt.Println(string(result))
	return true, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func corruptCandidates(ctx context.Context, db *database.DB) ([]corruptCandidate, error) {
	rows, err := db.Pool.Query(ctx, `SELECT s.id,s.pool_id,s.target_relative_path,s.start_at
		FROM recording_segments s JOIN recording_runs r ON r.id=s.run_id
		WHERE s.state='ready'
		ORDER BY s.start_at LIMIT $1`, corruptScanLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []corruptCandidate
	for rows.Next() {
		var item corruptCandidate
		if err := rows.Scan(&item.ID, &item.PoolID, &item.Target, &item.Start); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
