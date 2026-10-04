package recording

import (
	"github.com/zhigu34/one-nvr/internal/id"
	"strings"
	"testing"
	"time"
)

func TestRecordingPathTimezoneAndIdentity(t *testing.T) {
	site := id.ID("bb6f7f61-e7dd-4eee-a03c-a53167d6d688")
	pool := id.ID("c8f92b1a-22e5-4c8c-a3ca-7f66d8c2d101")
	run := id.ID("c8f92b1a-22e5-4c8c-a3ca-7f66d8c2d102")
	first, err := StableID(site, pool, run, ".work/zlm/known/record/one_nvr/stream/2026-10-04/first.mp4")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := StableID(site, pool, run, ".work/zlm/known/record/one_nvr/stream/2026-10-04/first.mp4")
	second, _ := StableID(site, pool, run, ".work/zlm/known/record/one_nvr/stream/2026-10-04/second.mp4")
	normalized, _ := StableID(id.ID(strings.ToUpper(string(site))), id.ID(strings.ToUpper(string(pool))), id.ID(strings.ToUpper(string(run))), ".work/zlm/known/record/one_nvr/stream/2026-10-04/first.mp4")
	if normalized != first {
		t.Fatal("UUID spelling changes logical recording identity")
	}
	if first != again || first == second || first[14] != '5' {
		t.Fatal("recording identity not stable UUIDv5")
	}
	start := time.Date(2026, 10, 3, 16, 4, 39, 0, time.UTC)
	frozen, err := FreezePath(1, start, "Asia/Shanghai", first)
	if err != nil {
		t.Fatal(err)
	}
	want := "recordings/CH01/2026-10-04/CH01_20261004_000439_" + strings.ReplaceAll(string(first), "-", "") + ".mp4"
	if frozen.RelativePath != want || frozen.UTCOffsetSeconds != 8*3600 || frozen.Timezone != "Asia/Shanghai" || !frozen.OriginalStart.Equal(start) {
		t.Fatal("incorrect frozen local naming", frozen)
	}
	utc, err := FreezePath(1, start, "UTC", first)
	if err != nil || frozen.RelativePath == utc.RelativePath {
		t.Fatal("timezone ignored")
	}
	for _, path := range []string{"../unknown.mp4", "/absolute.mp4", ".work/a/../b.mp4", ".work/link\n.mp4", ".work/.unfinished.mp4"} {
		if _, err := StableID(site, pool, run, path); err == nil {
			t.Fatal("invalid original path accepted", path)
		}
	}
	if _, err := FreezePath(0, start, "Asia/Shanghai", first); err == nil {
		t.Fatal("zero channel accepted")
	}
	if _, err := FreezePath(1, start, "Unrecognized/Zone", first); err == nil {
		t.Fatal("unknown timezone accepted")
	}
	// Both occurrences of a DST repeated hour still use distinct source identities.
	a := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	b := a.Add(time.Hour)
	pa, _ := FreezePath(1, a, "America/New_York", first)
	pb, _ := FreezePath(1, b, "America/New_York", second)
	if pa.RelativePath == pb.RelativePath || pa.UTCOffsetSeconds == pb.UTCOffsetSeconds {
		t.Fatal("DST repeats collided")
	}
}
