package recording

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

func videoFrames(snapshot zlm.StreamSnapshot) int64 {
	var frames int64
	for _, track := range snapshot.Tracks {
		if track.Ready && track.Width > 0 && track.Height > 0 && track.Frames > frames {
			frames = track.Frames
		}
	}
	return frames
}
func (s *Service) sampleBitrate(ctx context.Context, e *channel.Execution, ss physicalSession, test id.ID, decoded bool) (bool, error) {
	if err := e.Check(ctx); err != nil {
		return false, err
	}
	snapshot, err := s.Media.Inspect(ctx, ss.Key)
	if err != nil {
		return false, err
	}
	frames := videoFrames(snapshot)
	valid := frames > 0 && snapshot.BytesPerSecond > 0
	if test != "" && snapshot.Recording {
		valid = false
	}
	err = e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if !decoded {
			var previousFrames int64
			var observed time.Time
			err := tx.QueryRow(ctx, "SELECT frames,observed_at FROM recording_bitrate_samples WHERE stream_session_id=$1 ORDER BY observed_at DESC LIMIT 1", ss.ID).Scan(&previousFrames, &observed)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			// Individual samples describe positive frame progress. The capacity gate
			// independently requires two fresh samples at least ten seconds apart.
			valid = valid && err == nil && frames > previousFrames && snapshot.ObservedAt.After(observed)
		}
		sample, err := id.New()
		if err != nil {
			return err
		}
		var testID *id.ID
		if test != "" {
			testID = &test
		}
		_, err = tx.Exec(ctx, `INSERT INTO recording_bitrate_samples(id,channel_id,source_revision_id,stream_session_id,source_test_id,bytes_per_second,frames,valid,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, sample, e.ChannelID, ss.RevisionID, ss.ID, testID, snapshot.BytesPerSecond, max(0, frames), valid, snapshot.ObservedAt)
		return err
	})
	return valid, err
}
func (s *Service) sampleBitratePair(ctx context.Context, e *channel.Execution, ss physicalSession, test id.ID) error {
	// A decoded first frame has already been verified by the caller. Give the
	// media speed counter a bounded window to become nonzero before its first sample.
	initial, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		valid, err := s.sampleBitrate(initial, e, ss, test, true)
		if err != nil {
			return err
		}
		if valid {
			break
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-initial.Done():
			timer.Stop()
			return fault.New(409, "bitrate_unknown", "主流码率证据不可用")
		case <-timer.C:
		}
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	valid, err := s.sampleBitrate(ctx, e, ss, test, false)
	if err != nil {
		return err
	}
	if !valid {
		return fault.New(409, "bitrate_unknown", "未观察到主流帧进展和有效码率")
	}
	return nil
}
