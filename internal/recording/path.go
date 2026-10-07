package recording

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"

	"github.com/zhigu34/one-nvr/internal/id"
)

func StableID(siteID, poolID, runID id.ID, originalRelativePath string) (id.ID, error) {
	values := []*id.ID{&siteID, &poolID, &runID}
	for _, value := range values {
		parsed, err := id.Parse(string(*value))
		if err != nil {
			return "", err
		}
		*value = parsed
	}
	path := originalRelativePath
	if path == "" || len(path) > 4096 || filepath.IsAbs(path) || filepath.Clean(path) != path || path == ".." || strings.HasPrefix(path, "../") || filepath.Ext(path) != ".mp4" || strings.HasPrefix(filepath.Base(path), ".") || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return "", ErrCompletionInvalid
	}
	namespace, _ := hex.DecodeString(strings.ReplaceAll(string(siteID), "-", ""))
	name, _ := json.Marshal([]string{string(poolID), string(runID), path})
	hash := sha1.New()
	hash.Write(namespace)
	hash.Write(name)
	sum := hash.Sum(nil)[:16]
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(sum)
	return id.ID(encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]), nil
}
func FreezePath(channelNo int, start time.Time, timezone string, recordingID id.ID) (FrozenPath, error) {
	if channelNo < 1 || channelNo > 32 || start.IsZero() || start.Year() < 1970 || start.Year() > 9999 {
		return FrozenPath{}, ErrCompletionInvalid
	}
	normalized, err := id.Parse(string(recordingID))
	if err != nil {
		return FrozenPath{}, err
	}
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return FrozenPath{}, ErrCompletionInvalid
	}
	local := start.In(zone)
	_, offset := local.Zone()
	channel := fmt.Sprintf("CH%02d", channelNo)
	path := fmt.Sprintf("recordings/%s/%s/%s_%s_%s.mp4", channel, local.Format("2006-01-02"), channel, local.Format("20060102_150405"), strings.ReplaceAll(string(normalized), "-", ""))
	return FrozenPath{RelativePath: path, Timezone: timezone, UTCOffsetSeconds: offset, OriginalStart: start.UTC()}, nil
}
