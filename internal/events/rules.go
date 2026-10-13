package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Rule errors. A rule that cannot mean anything useful is refused at save
// time rather than left to silently never match.
var (
	ErrRuleInvalid = fault.New(422, "rule_invalid", "规则无效：请检查类型、区域、类别与时段")
	ErrRuleUnknown = auth.ErrNotFound
	// The detector keeps one dwell threshold per zone and gates both "present"
	// and "has entered" on it. Two rules that disagree about the threshold, or
	// an entry rule on a zone whose entry is delayed by one, would silently
	// mean something other than what was written.
	ErrRuleDwellConflict = fault.New(422, "rule_dwell_conflict", "同一区域只能有一个停留阈值：请改掉已有停留规则的秒数，或为这条规则换一个区域")
	ErrRuleEnterOnDwell  = fault.New(422, "rule_enter_on_dwell_zone", "该区域已有停留规则，检测器会把「进入」也延迟到停留时长之后；请为进入规则另建区域，或删除该停留规则")
)

// Rule kinds. They describe business interest; the zone semantics behind them
// belong to the detector:
//
//	appear — any detection of the categories
//	enter  — the detector reports the zone in entered_zones
//	dwell  — enter plus a minimum stay, which the detector enforces as loitering
type Rule struct {
	ID          id.ID    `json:"id"`
	ChannelID   id.ID    `json:"channel_id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	ZoneID      *id.ID   `json:"zone_id"`
	Categories  []string `json:"categories"`
	MinScore    float64  `json:"min_score"`
	ArmFrom     string   `json:"arm_from"`
	ArmTo       string   `json:"arm_to"`
	DwellSecond *int     `json:"dwell_seconds"`
	Severity    string   `json:"severity"`
	Cooldown    int      `json:"cooldown_seconds"`
	Enabled     bool     `json:"enabled"`
	Version     int64    `json:"version"`
}

// RuleHit is one evaluated match, with the facts that produced it frozen at
// match time: a later rule edit must not rewrite why an event was flagged.
type RuleHit struct {
	ID            id.ID           `json:"id"`
	EventID       id.ID           `json:"event_id"`
	RuleID        id.ID           `json:"rule_id"`
	RuleName      string          `json:"rule_name"`
	Severity      string          `json:"severity"`
	ZoneName      string          `json:"zone_name"`
	Reason        json.RawMessage `json:"reason"`
	NotifiedAt    *time.Time      `json:"notified_at"`
	FalsePositive bool            `json:"false_positive"`
	Note          string          `json:"false_positive_note"`
	CreatedAt     time.Time       `json:"created_at"`
}

const ruleColumns = `r.id,r.channel_id,r.name,r.kind,r.zone_id,r.categories,r.min_score,COALESCE(r.arm_from,''),COALESCE(r.arm_to,''),r.dwell_seconds,r.severity,r.cooldown_seconds,r.enabled,r.version`

func scanRule(row pgx.Row) (Rule, error) {
	var out Rule
	var categories []byte
	err := row.Scan(&out.ID, &out.ChannelID, &out.Name, &out.Kind, &out.ZoneID, &categories, &out.MinScore, &out.ArmFrom, &out.ArmTo, &out.DwellSecond, &out.Severity, &out.Cooldown, &out.Enabled, &out.Version)
	if err != nil {
		return out, err
	}
	out.Categories = []string{}
	_ = json.Unmarshal(categories, &out.Categories)
	return out, nil
}

// ListRules returns one channel's rules.
func (s *Service) ListRules(ctx context.Context, p auth.Principal, channelID id.ID) ([]Rule, error) {
	if s.Auth == nil {
		return nil, auth.ErrNotFound
	}
	if err := s.Auth.RequireChannel(ctx, p, channelID, auth.Playback); err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			return nil, auth.ErrNotFound
		}
		return nil, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT `+ruleColumns+` FROM detection_rules r WHERE r.channel_id=$1 ORDER BY r.name`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Rule, 0)
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// UpsertRule creates or replaces a rule. A rule whose conditions cannot be
// satisfied (a zone rule without a zone, a dwell rule without a threshold, a
// malformed arming window) is refused instead of stored as a rule that never
// fires.
func (s *Service) UpsertRule(ctx context.Context, p auth.Principal, in Rule) (Rule, error) {
	if s.Auth == nil {
		return Rule{}, auth.ErrNotFound
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 64 {
		return Rule{}, ErrRuleInvalid
	}
	if in.Kind != "appear" && in.Kind != "enter" && in.Kind != "dwell" {
		return Rule{}, ErrRuleInvalid
	}
	if len(in.Categories) == 0 || len(in.Categories) > 32 {
		return Rule{}, ErrRuleInvalid
	}
	if in.MinScore < 0 || in.MinScore > 1 {
		return Rule{}, ErrRuleInvalid
	}
	if in.Severity != "low" && in.Severity != "medium" && in.Severity != "high" {
		return Rule{}, ErrRuleInvalid
	}
	if in.Cooldown < 0 || in.Cooldown > 86400 {
		return Rule{}, ErrRuleInvalid
	}
	switch in.Kind {
	case "appear":
		if in.ZoneID != nil {
			return Rule{}, ErrRuleInvalid
		}
	case "enter":
		if in.ZoneID == nil {
			return Rule{}, ErrRuleInvalid
		}
	case "dwell":
		if in.ZoneID == nil || in.DwellSecond == nil || *in.DwellSecond < 1 || *in.DwellSecond > 86400 {
			return Rule{}, ErrRuleInvalid
		}
	}
	if (in.ArmFrom == "") != (in.ArmTo == "") {
		return Rule{}, ErrRuleInvalid
	}
	if in.ArmFrom != "" && (!validClock(in.ArmFrom) || !validClock(in.ArmTo)) {
		return Rule{}, ErrRuleInvalid
	}
	categories, err := json.Marshal(in.Categories)
	if err != nil {
		return Rule{}, ErrRuleInvalid
	}
	var out Rule
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, in.ChannelID, auth.Configure, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		// A zone rule must name a zone of this channel: a rule pointing at
		// another channel's zone could never match.
		ruleID := in.ID
		if in.ZoneID != nil {
			var belongs bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channel_zones WHERE id=$1 AND channel_id=$2)`, *in.ZoneID, in.ChannelID).Scan(&belongs); err != nil {
				return err
			}
			if !belongs {
				return ErrRuleInvalid
			}
			// The detector holds one dwell threshold per zone and applies it to
			// both "present" and "entered". Refusing the combinations it cannot
			// distinguish keeps a saved rule meaning what it says instead of
			// quietly firing late, or late for a reason the operator never wrote.
			// A rule being created has no id yet: NULL never matches an id, so
			// it excludes nothing, which is what an empty id must mean.
			exclude := any(nil)
			if ruleID != "" {
				exclude = ruleID
			}
			var existingDwell *int
			err := tx.QueryRow(ctx, `SELECT dwell_seconds FROM detection_rules
				WHERE zone_id=$1 AND kind='dwell' AND dwell_seconds IS NOT NULL AND id IS DISTINCT FROM $2 LIMIT 1`, *in.ZoneID, exclude).Scan(&existingDwell)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if in.Kind == "enter" && existingDwell != nil {
				return ErrRuleEnterOnDwell
			}
			if existingDwell != nil && (in.DwellSecond == nil || *existingDwell != *in.DwellSecond) {
				return ErrRuleDwellConflict
			}
			// The reverse order matters just as much: adding a dwell threshold
			// to a zone would retroactively delay entry rules that already exist
			// on it.
			if in.Kind == "dwell" {
				var entries int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM detection_rules
					WHERE zone_id=$1 AND kind='enter' AND id IS DISTINCT FROM $2`, *in.ZoneID, exclude).Scan(&entries); err != nil {
					return err
				}
				if entries > 0 {
					return ErrRuleEnterOnDwell
				}
			}
		}
		if ruleID == "" {
			ruleID, err = id.New()
			if err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO detection_rules(id,channel_id,name,kind,zone_id,categories,min_score,arm_from,arm_to,dwell_seconds,severity,cooldown_seconds,enabled)
			VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),$10,$11,$12,$13)
			ON CONFLICT (id) DO UPDATE SET name=$3,kind=$4,zone_id=$5,categories=$6,min_score=$7,arm_from=NULLIF($8,''),arm_to=NULLIF($9,''),dwell_seconds=$10,severity=$11,cooldown_seconds=$12,enabled=$13,version=detection_rules.version+1,updated_at=now()`,
			ruleID, in.ChannelID, in.Name, in.Kind, in.ZoneID, categories, in.MinScore, in.ArmFrom, in.ArmTo, in.DwellSecond, in.Severity, in.Cooldown, in.Enabled); err != nil {
			if strings.Contains(err.Error(), "detection_rules_channel_id_name_key") {
				return ErrRuleInvalid
			}
			return err
		}
		details, _ := json.Marshal(map[string]any{"name": in.Name, "kind": in.Kind, "severity": in.Severity})
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "rule.saved", ObjectID: ruleID, Details: details}); err != nil {
			return err
		}
		rule, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleColumns+` FROM detection_rules r WHERE r.id=$1`, ruleID))
		out = rule
		return err
	})
	if err != nil {
		return Rule{}, err
	}
	return out, nil
}

// DeleteRule removes a rule; its past hits go with it, because a hit is
// meaningless without the rule that produced it.
func (s *Service) DeleteRule(ctx context.Context, p auth.Principal, channelID, ruleID id.ID) error {
	if s.Auth == nil {
		return auth.ErrNotFound
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM detection_rules WHERE id=$1 AND channel_id=$2`, ruleID, channelID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrNotFound
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "rule.deleted", ObjectID: ruleID})
	})
}

// MarkFalsePositive records an operator's judgement on one hit. It is
// operational feedback: it retrains nothing, and it keeps the original reasons
// intact so the feedback stays auditable.
func (s *Service) MarkFalsePositive(ctx context.Context, p auth.Principal, hitID id.ID, note string, falsePositive bool) error {
	if s.Auth == nil {
		return auth.ErrNotFound
	}
	if len(note) > 500 {
		return auth.ErrInvalid
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var channelID id.ID
		if err := tx.QueryRow(ctx, `SELECT e.channel_id FROM event_rule_hits h JOIN events e ON e.id=h.event_id WHERE h.id=$1`, hitID).Scan(&channelID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Playback, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE event_rule_hits SET false_positive=$2,false_positive_note=$3 WHERE id=$1`, hitID, falsePositive, note); err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"false_positive": falsePositive})
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "rule_hit.marked", ObjectID: hitID, Details: details})
	})
}

// HitsForEvent returns the rule matches of one event, newest rule name first.
func (s *Service) HitsForEvent(ctx context.Context, eventID id.ID) ([]RuleHit, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT h.id,h.event_id,h.rule_id,r.name,r.severity,h.zone_name,h.reason,h.notified_at,h.false_positive,h.false_positive_note,h.created_at
		FROM event_rule_hits h JOIN detection_rules r ON r.id=h.rule_id WHERE h.event_id=$1 ORDER BY h.created_at`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RuleHit{}
	for rows.Next() {
		var hit RuleHit
		if err := rows.Scan(&hit.ID, &hit.EventID, &hit.RuleID, &hit.RuleName, &hit.Severity, &hit.ZoneName, &hit.Reason, &hit.NotifiedAt, &hit.FalsePositive, &hit.Note, &hit.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, hit)
	}
	return out, rows.Err()
}

func validClock(value string) bool {
	if len(value) != 5 || value[2] != ':' {
		return false
	}
	hour := (value[0]-'0')*10 + (value[1] - '0')
	minute := (value[3]-'0')*10 + (value[4] - '0')
	return value[0] >= '0' && value[0] <= '9' && value[1] >= '0' && value[1] <= '9' &&
		value[3] >= '0' && value[3] <= '9' && value[4] >= '0' && value[4] <= '9' &&
		hour <= 23 && minute <= 59
}

// withinArming reports whether the site-local wall clock falls inside the
// rule's window. An empty window arms always; a window whose end is not after
// its start crosses midnight (22:00–06:00), which is the common case for
// night rules.
func withinArming(rule Rule, local time.Time) bool {
	if rule.ArmFrom == "" {
		return true
	}
	now := local.Format("15:04")
	if rule.ArmFrom <= rule.ArmTo {
		return now >= rule.ArmFrom && now < rule.ArmTo
	}
	return now >= rule.ArmFrom || now < rule.ArmTo
}

// evaluateRules creates or refreshes the hits for one event. It runs inside
// the ingestion transaction, so an event and the fact that it tripped a rule
// are committed together.
//
// AI-05: one upstream target, one rule, one effective zone entry is one logical
// hit. The unique (event_id, rule_id) constraint is the fence; a later update
// of the same event refreshes the stored reason instead of adding a hit.
func (s *Service) evaluateRules(ctx context.Context, tx pgx.Tx, event Event) error {
	if event.ChannelID == nil {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT `+ruleColumns+`,COALESCE(z.name,'') FROM detection_rules r LEFT JOIN channel_zones z ON z.id=r.zone_id WHERE r.channel_id=$1 AND r.enabled`, *event.ChannelID)
	if err != nil {
		return err
	}
	type candidate struct {
		rule     Rule
		zoneName string
	}
	var rules []candidate
	for rows.Next() {
		var rule Rule
		var categories, zoneName []byte
		if err := rows.Scan(&rule.ID, &rule.ChannelID, &rule.Name, &rule.Kind, &rule.ZoneID, &categories, &rule.MinScore, &rule.ArmFrom, &rule.ArmTo, &rule.DwellSecond, &rule.Severity, &rule.Cooldown, &rule.Enabled, &rule.Version, &zoneName); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal(categories, &rule.Categories)
		rules = append(rules, candidate{rule: rule, zoneName: string(zoneName)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	local, err := s.siteLocal(event.StartedAt)
	if err != nil {
		return err
	}
	for _, entry := range rules {
		rule := entry.rule
		if !withinArming(rule, local) {
			continue
		}
		if event.Score < rule.MinScore {
			continue
		}
		if !containsString(rule.Categories, event.Category) {
			continue
		}
		if rule.Kind != "appear" && !containsString(event.Zones, entry.zoneName) {
			continue
		}
		reason, _ := json.Marshal(map[string]any{
			"kind":           rule.Kind,
			"category":       event.Category,
			"score":          event.Score,
			"zone":           entry.zoneName,
			"zones_reported": event.Zones,
			"severity":       rule.Severity,
		})
		hitID, err := id.New()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO event_rule_hits(id,event_id,rule_id,zone_name,reason) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (event_id,rule_id) DO UPDATE SET reason=EXCLUDED.reason,zone_name=EXCLUDED.zone_name`,
			hitID, event.ID, rule.ID, entry.zoneName, reason); err != nil {
			return err
		}
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// siteLocal converts an instant to the site's wall clock. Arming windows are
// written in site-local time, so the comparison must use the same clock the
// operator reads — never the container's own zone. The location is cached:
// ingestion evaluates rules for every event, and the zone changes at most once
// in a deployment's life.
func (s *Service) siteLocal(at time.Time) (time.Time, error) {
	if location := s.zone.Load(); location != nil {
		return at.In(location.(*time.Location)), nil
	}
	var zone string
	if err := s.DB.Pool.QueryRow(context.Background(), `SELECT timezone FROM sites WHERE id=$1`, s.siteID).Scan(&zone); err != nil {
		return time.Time{}, err
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, err
	}
	s.zone.Store(location)
	return at.In(location), nil
}
