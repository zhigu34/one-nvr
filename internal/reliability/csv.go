package reliability

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// CSV export (PRD REL-05).
//
// The requirement is specific about what the file must contain, and each part
// exists to stop a particular misreading:
//
//   - the denominator, so a percentage cannot be quoted without the thing it is
//     a fraction of;
//   - excluded time, so "not required to be up" cannot be read as "was up";
//   - unverified time, so time nobody could judge cannot be read as good;
//   - the metric version, so a figure can be identified as belonging to a
//     particular definition after the definition moves;
//   - the time zone, because a daily row means different instants in different
//     zones and a bare date is ambiguous.
//
// PRD REL-05 also says "sanitised": nothing here names a credential, a path or
// an address. The file describes a channel's observed history, not the site's
// internals.

// csvTZ explains the time zone in the file itself, because the reader of a CSV
// is often not the person who exported it.
const csvHeader = "指标版本,时区,通道号,通道名称,码流,日期,窗口开始,窗口结束,应录时长(秒),已覆盖时长(秒),缺口时长(秒),待核验时长(秒),排除时长(秒),可用时长(秒),不可用时长(秒),未知时长(秒),中断次数,最长中断(秒),数据精度\n"

// WriteComplianceCSV writes one channel's per-day reliability series.
//
// Per day rather than per interval: the reader is comparing days, and a row per
// interval would be a dump rather than a report. The daily rollup is the right
// source for exactly that reason — it is already the per-day projection of the
// same facts the metrics endpoint uses.
func (s *Service) WriteComplianceCSV(ctx context.Context, p auth.Principal, channelID id.ID, from, to time.Time, zone string, out io.Writer) error {
	if s.DB == nil || s.Auth == nil || channelID == "" || !to.After(from) {
		return auth.ErrInvalid
	}
	if err := s.Auth.RequireChannel(ctx, p, channelID, auth.Playback); err != nil {
		return err
	}
	var channelNo int
	var channelName string
	if err := s.DB.Pool.QueryRow(ctx, "SELECT channel_no,channel_name FROM channels WHERE id=$1", channelID).Scan(&channelNo, &channelName); err != nil {
		return err
	}
	if _, err := io.WriteString(out, csvHeader); err != nil {
		return err
	}
	// The location turns each stored UTC day into the operator's own day and
	// supplies the offset the file states.
	location, err := time.LoadLocation(zone)
	if err != nil {
		location = time.UTC
		zone = "UTC"
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT d.kind,d.day::text,
			d.healthy_ms,d.unavailable_ms,d.unknown_ms,d.excluded_ms,
			d.owed_ms,d.covered_ms,d.unverified_ms,d.fault_count,d.longest_fault_ms
		FROM reliability_daily d
		WHERE d.channel_id=$1 AND d.day >= $2::date AND d.day <= $3::date
		ORDER BY d.day,d.kind`,
		channelID, from.In(location).Format("2006-01-02"), to.In(location).Format("2006-01-02"))
	if err != nil {
		return err
	}
	defer rows.Close()
	writer := csv.NewWriter(out)
	// The header is written by hand so it can't be reordered by a struct change;
	// the writer below must emit fields in exactly that order.
	_ = writer
	for rows.Next() {
		var kind, day string
		var healthy, unavailable, unknown, excluded, owed, covered, unverified, longest int64
		var faults int
		if err := rows.Scan(&kind, &day, &healthy, &unavailable, &unknown, &excluded, &owed, &covered, &unverified, &faults, &longest); err != nil {
			return err
		}
		// Missing recording time is owed minus covered, and never negative: a
		// rollup can only be at most the whole day.
		missing := owed - covered - unverified
		if missing < 0 {
			missing = 0
		}
		record := []string{
			strconv.Itoa(MetricVersion),
			zone,
			strconv.Itoa(channelNo),
			channelName,
			kind,
			day,
			day + " 00:00",
			day + " 24:00",
			seconds(owed),
			seconds(covered),
			seconds(missing),
			seconds(unverified),
			seconds(excluded),
			seconds(healthy),
			seconds(unavailable),
			seconds(unknown),
			strconv.Itoa(faults),
			seconds(longest),
			// Precision is stated per row rather than once: a file may span days
			// answered from different tiers, and a reader must be able to tell
			// which rows those were.
			dayPrecision(day, location, time.Now().UTC()),
		}
		if err := writeCSVRecord(writer, record); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}

// writeCSVRecord writes one row, keeping every field a string so a malformed
// value cannot silently change the column count.
func writeCSVRecord(w *csv.Writer, record []string) error {
	return w.Write(record)
}

// seconds renders milliseconds as seconds with one decimal, which is the unit an
// operator thinks in and avoids a wall of digits.
func seconds(ms int64) string {
	return fmt.Sprintf("%.1f", float64(ms)/1000)
}

// dayPrecision labels a day by which retention tier still holds its detail.
//
// It asks how old the DAY is, not when the rollup was computed: a day whose
// detailed intervals have been swept cannot be drilled into no matter how
// recently its summary row was rewritten, and that is what the label is for.
func dayPrecision(day string, location *time.Location, now time.Time) string {
	when, err := time.ParseInLocation("2006-01-02", day, location)
	if err != nil {
		return PrecisionDaily
	}
	// A day is "exact" while its intervals are still retained; below that the
	// minute tier answers; beyond both only the daily rollup is left.
	age := now.Sub(when)
	switch {
	case age < IntervalRetention:
		return PrecisionExact
	case age < DailyRetention:
		return PrecisionMinute
	default:
		return PrecisionDaily
	}
}
