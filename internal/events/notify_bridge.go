package events

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/notify"
)

// raiseNotification bridges a rule hit to the notification package.
//
// The split is deliberate. This package knows what a hit is and holds the event
// row; the notify package knows what a message contains and how it is carried.
// The bridge gathers the facts (including the channel's number and name, which
// the event row does not carry) and hands them over, so neither side has to
// reach into the other's tables.
//
// It runs inside the ingestion transaction on purpose: a crash must not leave a
// hit the operator was never told about, nor a notification for a hit that was
// rolled back.
func (s *Service) raiseNotification(ctx context.Context, tx pgx.Tx, event Event, rule Rule, zoneName string, hitID id.ID) error {
	if s.Notify == nil || event.ChannelID == nil {
		// Detection is wired without notification in some assemblies (and in
		// every test that only exercises rule matching). Recording the hit is
		// the contract; telling is an extension.
		return nil
	}
	facts, err := s.notificationFacts(ctx, tx, event, rule, zoneName, hitID)
	if err != nil {
		return err
	}
	_, err = s.Notify.Raise(ctx, tx, facts)
	return err
}

func (s *Service) notificationFacts(ctx context.Context, tx pgx.Tx, event Event, rule Rule, zoneName string, hitID id.ID) (notify.HitFacts, error) {
	var facts notify.HitFacts
	// The channel number is what an operator reads and the name is how they
	// recognize it; neither is on the event row, and a notification naming a
	// UUID would be useless.
	var channelNo int
	var channelName string
	var siteName string
	if err := tx.QueryRow(ctx, `SELECT c.channel_no,c.channel_name,si.name
		FROM channels c JOIN sites si ON si.id=c.site_id WHERE c.id=$1`, *event.ChannelID).
		Scan(&channelNo, &channelName, &siteName); err != nil {
		return facts, err
	}
	location, err := s.siteLocation(ctx)
	if err != nil {
		return facts, err
	}
	facts = notify.HitFacts{
		HitID:           hitID,
		EventID:         event.ID,
		RuleID:          rule.ID,
		RuleName:        rule.Name,
		Severity:        rule.Severity,
		CooldownSeconds: rule.Cooldown,
		Message: notify.MessageInput{
			EventID:     event.ID,
			ChannelID:   *event.ChannelID,
			ChannelNo:   channelNo,
			ChannelName: channelName,
			SiteName:    siteName,
			Category:    event.Category,
			Score:       event.Score,
			Zone:        zoneName,
			RuleID:      rule.ID,
			RuleName:    rule.Name,
			Severity:    rule.Severity,
			StartedAt:   event.StartedAt,
			EndedAt:     event.EndedAt,
			Location:    location,
			PublicURL:   s.PublicURL,
			// A pending snapshot still gets a link: the endpoint reports
			// "not generated yet" and the picture appears once the detector
			// writes it, which is more useful than a message with no way to
			// look at all.
			SnapshotAvailable: event.SnapshotState != "gone",
		},
	}
	return facts, nil
}

// siteLocation resolves the site's timezone for rendering the message's local
// time. It reuses the zone cache the arming windows already maintain, so an
// event and its notification cannot disagree about what time it is.
func (s *Service) siteLocation(ctx context.Context) (*time.Location, error) {
	local, err := s.siteLocal(s.now().UTC())
	if err != nil {
		return nil, err
	}
	return local.Location(), nil
}
