package recording

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// Deleting a local copy removes only the file in the directory pool. The index
// row survives with state 'deleted', the gap evidence stays, and no remote copy
// is touched: PRD FILE-04 requires the media to become unplayable while the
// record of it having existed remains auditable.
var (
	ErrDeleteProtected = fault.New(409, "recording_protected", "录像已保护，需先解除保护")
	ErrDeleteNotReady  = fault.New(409, "recording_not_ready", "录像尚未完成发布，无法删除")
	ErrDeleteWorking   = fault.New(409, "recording_in_use", "录像正在写入或正在播放，稍后再试")
)

// DeleteOutcome is the per-item result of one delete request, shaped by what the
// operator must be told: why an item was kept and how much space the rest freed.
type DeleteOutcome struct {
	SegmentID id.ID  `json:"segment_id"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
}

const (
	deleteStateDeleted   = "deleted"
	deleteStateProtected = "protected"
	deleteStateNotReady  = "not_ready"
	deleteStateMissing   = "missing"
	deleteStateSkipped   = "in_use"
	deleteStateUnknown   = "not_found"
)

// FreshnessDelay keeps automatic retention away from the recording that was just
// published: a sweep should not delete the file a segment is still settling
// into, and it is deliberately far larger than the 60s segment target.
//
// It deliberately does NOT apply to an administrator's explicit deletion. A row
// only becomes deletable once its publication completed (state 'ready'), which
// already means the file is whole and nothing is still writing to it, so the
// delay would only refuse an action the operator just asked for by name.
const FreshnessDelay = 10 * time.Minute

// DeleteLocal removes the local copy of each requested segment and reports one
// outcome per item. A rejected item never blocks or rolls back an accepted one;
// the caller sees exactly which files were removed and how many bytes that
// released.
func (s *Service) DeleteLocal(ctx context.Context, p auth.Principal, segmentIDs []id.ID, reason string) ([]DeleteOutcome, error) {
	if len(segmentIDs) == 0 || len(segmentIDs) > 100 || (reason != "manual" && reason != "retention") {
		return nil, auth.ErrInvalid
	}
	if s.Auth == nil || s.Pools == nil {
		return nil, auth.ErrNotFound
	}
	// Deleting recordings is an administrator operation (PRD §3): the channel
	// grant model decides who may watch, never who may destroy evidence.
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	return s.deleteLocal(ctx, p.UserID, segmentIDs, reason), nil
}

// deleteLocal is the deletion core, shared by the administrator path and the
// retention sweep. The actor is recorded as given and is empty for a
// system-initiated deletion, which the audit log stores as "no user" rather
// than attributing automatic expiry to an operator who did not ask for it.
func (s *Service) deleteLocal(ctx context.Context, actor id.ID, segmentIDs []id.ID, reason string) []DeleteOutcome {
	out := make([]DeleteOutcome, 0, len(segmentIDs))
	for _, segmentID := range segmentIDs {
		outcome, err := s.deleteOne(ctx, actor, segmentID, reason)
		if err != nil {
			// A storage fault (pool gone, media unreadable) is reported on its
			// own row so the remaining files are still processed.
			outcome = DeleteOutcome{SegmentID: segmentID, State: deleteStateSkipped, Reason: storageReason(err)}
		}
		out = append(out, outcome)
	}
	return out
}

func storageReason(err error) string {
	var public *fault.Error
	if errors.As(err, &public) {
		return public.Code
	}
	if errors.Is(err, storage.ErrMediaProof) {
		return "pool_unavailable"
	}
	return "unavailable"
}

// deleteOne performs the whole lifecycle for a single segment: decide, unlink,
// then record. The file is removed before the row changes state, so an
// interruption in between leaves a row that still claims 'ready' over a file
// that is gone — which the existing reconciliation (List, pool usage) reports as
// 'missing'. That is exactly the distinction PRD FILE-04 draws between a
// deliberate deletion ('deleted') and a file that disappeared outside one-nvr
// ('missing'); the reverse order would let a crash hide a real deletion.
func (s *Service) deleteOne(ctx context.Context, actor id.ID, segmentID id.ID, reason string) (DeleteOutcome, error) {
	var item publication
	pinnedByExport, pinnedByArchive := false, false
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		found, err := scanPublication(tx.QueryRow(ctx, `SELECT `+publicationColumns+` FROM recording_segments s WHERE s.id=$1`, segmentID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		item = found
		// EXP-05: a segment pinned by a queued or running export is that
		// task's input. Removing it mid-task would either fail the export or
		// silently shorten its artifact, so deletion waits for the task. The
		// same holds while an archive upload is in flight (M2.1 ARC-02).
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recording_export_sources es JOIN recording_exports e ON e.id=es.export_id WHERE es.segment_id=$1 AND e.state IN ('queued','running'))`, segmentID).Scan(&pinnedByExport); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recording_archive_copies c WHERE c.segment_id=$1 AND c.state='uploading')`, segmentID).Scan(&pinnedByArchive)
	})
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			return DeleteOutcome{SegmentID: segmentID, State: deleteStateUnknown, Reason: "not_found"}, nil
		}
		return DeleteOutcome{}, err
	}
	switch {
	case pinnedByExport:
		return DeleteOutcome{SegmentID: segmentID, State: deleteStateSkipped, Reason: "pinned_by_export"}, nil
	case pinnedByArchive:
		return DeleteOutcome{SegmentID: segmentID, State: deleteStateSkipped, Reason: "archiving"}, nil
	case item.Protected:
		return DeleteOutcome{SegmentID: segmentID, State: deleteStateProtected, Reason: "protected"}, nil
	case item.State == deleteStateDeleted:
		// Already deleted is a success, not an error: a retried request (or a
		// second administrator) must not see a failure for work that is done.
		return DeleteOutcome{SegmentID: segmentID, State: deleteStateDeleted}, nil
	case item.State == "missing":
		return DeleteOutcome{SegmentID: segmentID, State: deleteStateMissing, Reason: "missing"}, nil
	case item.State != "ready":
		return DeleteOutcome{SegmentID: segmentID, State: deleteStateNotReady, Reason: item.State}, nil
	}
	// Automatic retention stays away from a recording that has only just been
	// published; it re-runs, so postponing by one round costs nothing. Manual
	// deletion is an explicit choice about a named row and skips this.
	if reason == "retention" && time.Since(item.End) < FreshnessDelay {
		return DeleteOutcome{SegmentID: segmentID, State: deleteStateSkipped, Reason: "recent"}, nil
	}
	root, _, err := s.Pools.OpenMediaRoot(ctx, item.PoolID)
	if err != nil {
		return DeleteOutcome{}, err
	}
	defer root.Close()
	// Verify the published identity before unlinking: a path that no longer
	// matches its record must not authorize deleting whatever now sits there.
	file, err := storage.OpenMediaFile(root, item.Target)
	if err != nil {
		if _, statErr := root.Lstat(item.Target); os.IsNotExist(statErr) {
			return DeleteOutcome{SegmentID: segmentID, State: deleteStateMissing, Reason: "missing"}, nil
		}
		return DeleteOutcome{}, err
	}
	info, err := file.Stat()
	if err != nil || !item.Identity.matches(info) {
		file.Close()
		return DeleteOutcome{SegmentID: segmentID, State: deleteStateSkipped, Reason: "identity_mismatch"}, nil
	}
	releaseBytes := info.Size()
	file.Close()
	if err := root.Remove(item.Target); err != nil {
		if os.IsNotExist(err) {
			return DeleteOutcome{SegmentID: segmentID, State: deleteStateMissing, Reason: "missing"}, nil
		}
		return DeleteOutcome{}, err
	}
	// Only now record the outcome. An audit failure must not leave the file
	// deleted but the index claiming otherwise, so both happen in one
	// transaction and the file is already gone either way.
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE recording_segments SET state='deleted',error_code=NULL WHERE id=$1 AND state='ready'`, segmentID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE recording_locations SET state='deleted',deleted_at=clock_timestamp(),delete_reason=$2,verified_at=clock_timestamp() WHERE segment_id=$1`, segmentID, reason); err != nil {
			return err
		}
		details, err := json.Marshal(map[string]any{
			"channel_id":   item.ChannelID,
			"released":     releaseBytes,
			"delete_kind":  reason,
			"segment_from": item.Start.UTC(),
			"segment_to":   item.End.UTC(),
		})
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: actor, Action: "recording.deleted_local", ObjectID: segmentID, Details: details})
	})
	if err != nil {
		return DeleteOutcome{}, err
	}
	return DeleteOutcome{SegmentID: segmentID, State: deleteStateDeleted, Bytes: releaseBytes}, nil
}

// SetProtection is evidence preservation: while it is set, retention and
// operator deletion both skip the segment. PRD FILE-03 requires both directions
// to be auditable, so the state change and its audit row travel together.
func (s *Service) SetProtection(ctx context.Context, p auth.Principal, segmentID id.ID, protected bool) (Segment, error) {
	var out Segment
	if s.Auth == nil {
		return out, auth.ErrNotFound
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		item, err := scanPublication(tx.QueryRow(ctx, `SELECT `+publicationColumns+` FROM recording_segments s WHERE s.id=$1`, segmentID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		if item.State == deleteStateDeleted {
			return ErrDeleteNotReady
		}
		if _, err := tx.Exec(ctx, `UPDATE recording_segments SET protected=$2,protected_at=CASE WHEN $2 THEN clock_timestamp() ELSE NULL END WHERE id=$1`, segmentID, protected); err != nil {
			return err
		}
		action := "recording.unprotected"
		if protected {
			action = "recording.protected"
		}
		details, err := json.Marshal(map[string]any{"channel_id": item.ChannelID})
		if err != nil {
			return err
		}
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: action, ObjectID: segmentID, Details: details}); err != nil {
			return err
		}
		item.Protected = protected
		item.Segment.Protected = protected
		out = item.Segment
		return nil
	})
	return out, err
}
