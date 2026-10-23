package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
)

// exclusionFor reports whether an event falls inside one of its channel's
// exclusions, and which one.
//
// The zone the detector reported is NOT consulted. An exclusion is a polygon the
// operator drew, and the detector only reports the zones it was told about — so
// matching on `entered_zones` would only ever catch exclusions that were also
// published to the detector as zones, which they deliberately are not (publishing
// them would change what the detector tracks and, for a loitering zone, when).
// The box is the fact both sides share, so the test is geometric.
func (s *Service) exclusionFor(ctx context.Context, tx pgx.Tx, channelID id.ID, event Event) (bool, *id.ID, string, error) {
	rows, err := tx.Query(ctx, `SELECT id,name,coordinates,categories,COALESCE(from_clock,''),COALESCE(to_clock,''),reason
		FROM detection_exclusions WHERE channel_id=$1 AND enabled`, channelID)
	if err != nil {
		return false, nil, "", err
	}
	defer rows.Close()
	local := event.StartedAt
	if zone := s.siteZone(); zone != nil {
		local = event.StartedAt.In(zone)
	}
	for rows.Next() {
		var (
			exclusion Exclusion
			rawCoords []byte
			rawCats   []byte
		)
		if err := rows.Scan(&exclusion.ID, &exclusion.Name, &rawCoords, &rawCats,
			&exclusion.FromClock, &exclusion.ToClock, &exclusion.Reason); err != nil {
			return false, nil, "", err
		}
		if err := json.Unmarshal(rawCoords, &exclusion.Coordinates); err != nil {
			// A stored drawing that cannot be parsed cannot be evaluated. Skipping
			// it keeps the event visible, which is the safe direction: a broken
			// exclusion must not silently suppress detections.
			continue
		}
		if err := json.Unmarshal(rawCats, &exclusion.Categories); err != nil {
			continue
		}
		if exclusion.covers(event.Category, event.Box, local) {
			ruleID := id.ID(exclusion.ID)
			return true, &ruleID, exclusion.Reason, nil
		}
	}
	return false, nil, "", rows.Err()
}

// siteZone returns the site's time zone for wall-clock comparisons.
//
// An exclusion window is a local wall-clock statement ("quiet between 22:00 and
// 07:00"), so it must be evaluated in the site's zone, not UTC. A site whose zone
// cannot be read falls back to UTC and the windows shift — wrong, but visible;
// the alternative (refusing to evaluate) would turn every exclusion off.
func (s *Service) siteZone() *time.Location {
	if cached := s.zone.Load(); cached != nil {
		if loc, ok := cached.(*time.Location); ok && loc != nil {
			return loc
		}
	}
	if s.DB == nil || s.siteID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var name string
	if err := s.DB.Pool.QueryRow(ctx, `SELECT timezone FROM sites WHERE id=$1`, s.siteID).Scan(&name); err != nil {
		return nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	s.zone.Store(loc)
	return loc
}
