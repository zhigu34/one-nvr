package recording

// Purging all recorded history.
//
// This is a test-stage reset, not a retention feature: it removes every
// recording index row and every event, leaving configuration (channels, sources,
// pools, targets, policies, schedules, rules, users) and the audit log intact.
// Wiping configuration would turn a data reset into a re-install, and the audit
// log is the record that the deletion happened at all.
//
// It lives here rather than in the admin binary for one reason: the statement
// order is dictated by foreign keys, and the only way to know it is right is to
// run it against a real migrated schema. An order that is wrong fails halfway
// through a production purge, which is exactly the moment nobody wants to be
// debugging it.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/database"
)

// historyPurgeStatements deletes the whole media and event history.
//
// Order is children before parents. The non-obvious ones:
//
//   - hook_inbox has ON DELETE NO ACTION to recording_runs, so it must go before
//     them or the run delete fails;
//   - event_clip_jobs references both events and clip_windows;
//   - recording_export_sources references recording_segments with no cascade.
//
// Everything else either cascades or is a root, but every table is named
// explicitly: a purge that silently skips a table because a cascade happens to
// exist today would start leaking rows the moment someone drops that cascade.
var historyPurgeStatements = []string{
	`DELETE FROM event_clip_jobs`,
	`DELETE FROM clip_windows`,
	`DELETE FROM recording_playback_leases`,
	`DELETE FROM recording_export_sources`,
	`DELETE FROM recording_export_artifacts`,
	`DELETE FROM recording_exports`,
	`DELETE FROM recording_archive_copies`,
	`DELETE FROM recording_locations`,
	`DELETE FROM hook_inbox`,
	`DELETE FROM jobs WHERE kind LIKE 'recording.%'`,
	`DELETE FROM recording_segments`,
	`DELETE FROM recording_manual_windows`,
	`DELETE FROM recording_gaps`,
	`DELETE FROM recording_obligations`,
	`DELETE FROM recording_runs`,
	`DELETE FROM events`,
}

// PurgeHistoryRows empties the media and event index in one transaction.
//
// One transaction on purpose: a half-purged index is worse than a full one,
// because playback and reliability would keep reading from two different worlds.
// The per-file deletions that precede this call are each recorded in the audit
// log, so the evidence trail survives the rows it describes.
func PurgeHistoryRows(ctx context.Context, db *database.DB) error {
	return db.WithinTx(ctx, func(tx pgx.Tx) error {
		for _, statement := range historyPurgeStatements {
			if _, err := tx.Exec(ctx, statement); err != nil {
				return fmt.Errorf("%s: %w", statement, err)
			}
		}
		details, err := json.Marshal(map[string]any{"scope": "all_media_and_events"})
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{Action: "recording.history_purged", Details: details})
	})
}
