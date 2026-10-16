package reliability

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Root correlation, maintenance windows and attribution revision.
//
// These three share a property worth stating once: none of them changes the
// per-channel record. A root event aggregates children, a maintenance window
// reclassifies time that was already recorded, and an attribution revision adds
// an interpretation beside the automatic one. The underlying interval and fault
// rows stay exactly as observed, which is what makes an unfavourable record
// impossible to erase (PRD REL-02).

// CorrelateRoot groups open faults that share a cause into one component-level
// root event.
//
// The heuristic is deliberately narrow: faults count as one incident only when
// they are of the same fault kind and category AND started within a short
// window of each other. A media server that dies takes every channel down at
// the same instant; two channels failing an hour apart are two incidents even
// if the reasons match. Being narrow means occasionally missing a correlation,
// which costs a tidier report; being broad would merge unrelated outages and
// understate them, which would be a wrong number.
func (s *Service) CorrelateRoot(ctx context.Context) error {
	if s.DB == nil {
		return nil
	}
	// A component fault (storage, media_server, monitoring) is the root by
	// construction: it is not a single channel's problem.
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		roots, err := tx.Query(ctx, `
			SELECT id,kind,category,started_at FROM fault_events
			WHERE state='open' AND root_event_id IS NULL
				AND kind IN ('storage','media_server','monitoring','switch')
			FOR UPDATE`)
		if err != nil {
			return err
		}
		type root struct {
			id       id.ID
			kind     string
			category string
			started  time.Time
		}
		var candidates []root
		for roots.Next() {
			var r root
			if err := roots.Scan(&r.id, &r.kind, &r.category, &r.started); err != nil {
				roots.Close()
				return err
			}
			candidates = append(candidates, r)
		}
		roots.Close()
		if err := roots.Err(); err != nil {
			return err
		}
		for _, r := range candidates {
			// Attach the channel-level faults that share this component fault's
			// category and began within the correlation window.
			if _, err := tx.Exec(ctx, `UPDATE fault_events SET root_event_id=$1,updated_at=clock_timestamp()
				WHERE state='open' AND root_event_id IS NULL AND id<>$1
					AND category=$2 AND kind IN ('main','sub','recording')
					AND started_at BETWEEN $3 AND $4`,
				r.id, r.category, r.started.Add(-correlationWindow), r.started.Add(correlationWindow)); err != nil {
				return err
			}
		}
		return nil
	})
}

// correlationWindow bounds how far apart two faults may start and still be
// treated as one incident. One sampling period plus slack: a component failure
// surfaces on every channel at the next sample, not minutes later.
const correlationWindow = 30 * time.Second

// ImpactUnion returns the real business impact of a root event: the union of
// its children's intervals, not their sum.
//
// PRD REL-02 requires this explicitly, and the difference is large: eight
// channels down for five minutes is five minutes of site impact, not forty.
// Summing would inflate every component failure by the channel count.
func (s *Service) ImpactUnion(ctx context.Context, rootID id.ID) (time.Duration, error) {
	if s.DB == nil {
		return 0, nil
	}
	// The standard union of intervals: sweep the sorted endpoints and count a
	// stretch of time exactly once however many children cover it. Done in SQL
	// so the whole set is handled without loading it into memory.
	var seconds *float64
	err := s.DB.Pool.QueryRow(ctx, `
		WITH spans AS (
			SELECT started_at AS at, 1 AS delta FROM fault_events WHERE root_event_id=$1
			UNION ALL
			SELECT COALESCE(ended_at, clock_timestamp()) AS at, -1 AS delta
			FROM fault_events WHERE root_event_id=$1
		), ordered AS (
			SELECT at, SUM(delta) OVER (ORDER BY at, delta DESC) AS open_count
			FROM spans
		), segments AS (
			-- Each row's span runs to the next endpoint. open_count says whether
			-- anything was still failing across it.
			SELECT open_count, at AS segment_start,
				LEAD(at) OVER (ORDER BY at) AS segment_end
			FROM ordered
		)
		SELECT COALESCE(SUM(EXTRACT(EPOCH FROM (segment_end - segment_start))), 0)
		FROM segments
		WHERE open_count > 0 AND segment_end IS NOT NULL`, rootID).Scan(&seconds)
	if err != nil {
		return 0, err
	}
	if seconds == nil {
		return 0, nil
	}
	return time.Duration(*seconds * float64(time.Second)), nil
}

// MaintenanceWindow is a registered planned stop.
type MaintenanceWindow struct {
	ID        id.ID     `json:"id"`
	ChannelID *id.ID    `json:"channel_id"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

// RegisterMaintenance records a planned window. PRD REL-02 wants planned stops
// counted apart from real faults, and it also wants to be able to tell a
// forecast from a backfill — so the registration time is kept alongside the
// window, and a window registered after the fact is still honoured but visible
// as such in a report.
func (s *Service) RegisterMaintenance(ctx context.Context, p auth.Principal, in MaintenanceWindow) (MaintenanceWindow, error) {
	var out MaintenanceWindow
	if s.DB == nil || s.Auth == nil {
		return out, auth.ErrInvalid
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	if !in.EndsAt.After(in.StartsAt) {
		return out, auth.ErrInvalid
	}
	id_, err := id.New()
	if err != nil {
		return out, err
	}
	var channelID any
	if in.ChannelID != nil {
		channelID = *in.ChannelID
	}
	var createdBy any
	if p.UserID != "" {
		createdBy = p.UserID
	}
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO maintenance_windows(id,channel_id,starts_at,ends_at,note,created_by)
			VALUES($1,$2,$3,$4,$5,$6)`, id_, channelID, in.StartsAt.UTC(), in.EndsAt.UTC(), in.Note, createdBy); err != nil {
			return err
		}
		// Faults already recorded inside the window are reclassified, because
		// the operator is telling us they were planned. The change is additive:
		// the fault rows stay, only the maintenance flag moves.
		if _, err := tx.Exec(ctx, `UPDATE fault_events SET maintenance=true,updated_at=clock_timestamp()
			WHERE started_at < $2 AND COALESCE(ended_at,clock_timestamp()) > $1
				AND ($3::uuid IS NULL OR channel_id=$3)`,
			in.StartsAt.UTC(), in.EndsAt.UTC(), channelID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	out = in
	out.ID = id_
	out.CreatedAt = time.Now().UTC()
	return out, nil
}

// ListMaintenance returns windows overlapping a range, for the API.
func (s *Service) ListMaintenance(ctx context.Context, p auth.Principal, from, to time.Time) ([]MaintenanceWindow, error) {
	out := []MaintenanceWindow{}
	if s.DB == nil || s.Auth == nil {
		return out, auth.ErrInvalid
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,channel_id,starts_at,ends_at,note,created_at
		FROM maintenance_windows WHERE starts_at < $2 AND ends_at > $1 ORDER BY starts_at DESC`, from.UTC(), to.UTC())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item MaintenanceWindow
		if err := rows.Scan(&item.ID, &item.ChannelID, &item.StartsAt, &item.EndsAt, &item.Note, &item.CreatedAt); err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ConfirmFault lets an administrator record what actually happened.
//
// PRD REL-02: the automatic classification is never rewritten, the confirmation
// is added beside it, and each revision keeps its own record. An operator can
// therefore correct a wrong attribution without erasing the fact that the
// system originally said something else — which is what stops a quiet
// reclassification from improving the availability numbers.
func (s *Service) ConfirmFault(ctx context.Context, p auth.Principal, faultID id.ID, reason, category, note string) error {
	if s.DB == nil || s.Auth == nil {
		return auth.ErrInvalid
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return err
	}
	if category != "" && !validCategory(category) {
		return auth.ErrInvalid
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var originalCategory, originalReason string
		if err := tx.QueryRow(ctx, `SELECT category,initial_reason FROM fault_events WHERE id=$1 FOR UPDATE`, faultID).Scan(&originalCategory, &originalReason); err != nil {
			return err
		}
		revision, err := json.Marshal(map[string]any{
			"at":                time.Now().UTC().Format(time.RFC3339),
			"by":                p.UserID,
			"previous_category": originalCategory,
			"previous_reason":   originalReason,
			"category":          category,
			"reason":            reason,
			"note":              note,
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE fault_events SET
				confirmed_reason=COALESCE(NULLIF($2,''),confirmed_reason),
				confirmed_category=COALESCE(NULLIF($3,''),confirmed_category),
				note=$4,
				revisions=revisions || $5::jsonb,
				updated_at=clock_timestamp()
			WHERE id=$1`, faultID, reason, category, note, revision)
		return err
	})
}

func validCategory(category string) bool {
	switch category {
	case CategoryUpstream, CategoryAuthConfig, CategoryRecording, CategoryStorage,
		CategoryDetection, CategoryMonitoring, CategoryMaintenance, CategoryUnknown:
		return true
	}
	return false
}

// EffectiveCategory is what a report should count by: the confirmed one when an
// operator has ruled, otherwise the automatic one. The difference is visible in
// the row, so a reader can always tell which is which.
func EffectiveCategory(initial, confirmed string) string {
	if confirmed != "" {
		return confirmed
	}
	return initial
}
