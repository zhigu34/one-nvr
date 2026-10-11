package recording

import (
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

var exportTestBase = time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)

func exportTestSegment(no int, offset, duration int, state string, media MediaParams) sourceSegment {
	raw, _ := id.New()
	return sourceSegment{
		ID: raw, State: state,
		Start: exportTestBase.Add(time.Duration(offset) * time.Second),
		End:   exportTestBase.Add(time.Duration(offset+duration) * time.Second),
		Bytes: 1000, Media: media,
	}
}

func h264() MediaParams { return MediaParams{Codec: "h264", Width: 1920, Height: 1080, FPS: 25} }

func TestAnalyzeExportWindowGroupsAndGaps(t *testing.T) {
	segments := []sourceSegment{
		exportTestSegment(1, 0, 60, "ready", h264()),
		exportTestSegment(2, 60, 60, "ready", h264()),
		// A real hole: three minutes with nothing indexed.
		exportTestSegment(3, 300, 60, "ready", h264()),
	}
	plan := analyzeExportWindow(exportTestBase, exportTestBase.Add(10*time.Minute), segments)
	if len(plan.Groups) != 2 {
		t.Fatal("contiguous segments must form one group, the hole must split", len(plan.Groups))
	}
	if plan.Groups[0].Start != exportTestBase || plan.Groups[0].End != exportTestBase.Add(120*time.Second) {
		t.Fatal("first group bounds wrong", plan.Groups[0])
	}
	if len(plan.Gaps) != 2 {
		t.Fatal("hole plus trailing region", len(plan.Gaps))
	}
	if got := plan.Gaps[0]; got.Start != exportTestBase.Add(120*time.Second) || got.End != exportTestBase.Add(300*time.Second) {
		t.Fatal("hole bounds wrong", got)
	}
	if plan.coverage() != 180*time.Second {
		t.Fatal("coverage wrong", plan.coverage())
	}
	if plan.MediaConsistent != true || plan.ReadyBytes != 3000 {
		t.Fatal("aggregate wrong", plan.MediaConsistent, plan.ReadyBytes)
	}
}

func TestAnalyzeExportWindowAbsorbsKeyframeSeams(t *testing.T) {
	// A sub-second seam between published segments is a seam of continuous
	// recording, not a gap: it must neither split a group nor be reported.
	segments := []sourceSegment{
		exportTestSegment(1, 0, 60, "ready", h264()),
		exportTestSegment(2, 60, 60, "ready", h264()),
	}
	segments[1].Start = segments[1].Start.Add(300 * time.Millisecond)
	plan := analyzeExportWindow(exportTestBase, exportTestBase.Add(5*time.Minute), segments)
	if len(plan.Groups) != 1 {
		t.Fatal("a sub-second seam must stay inside one group", len(plan.Groups))
	}
	if len(plan.Gaps) != 1 || plan.Gaps[0].Start != exportTestBase.Add(120*time.Second) {
		t.Fatal("only the trailing region is a gap", plan.Gaps)
	}
}

func TestAnalyzeExportWindowDropsOverlapsWithoutDuplicates(t *testing.T) {
	segments := []sourceSegment{
		exportTestSegment(1, 0, 60, "ready", h264()),
		// A source switched mid-run: the re-published span overlaps the chain.
		exportTestSegment(2, 30, 60, "ready", h264()),
	}
	plan := analyzeExportWindow(exportTestBase, exportTestBase.Add(5*time.Minute), segments)
	if len(plan.Groups) != 1 || len(plan.Groups[0].Segments) != 1 {
		t.Fatal("overlap must not enter the chain twice", plan.Groups)
	}
	if plan.DuplicateCount != 1 {
		t.Fatal("the skipped duplicate must be counted", plan.DuplicateCount)
	}
}

func TestAnalyzeExportWindowListsUnavailableSegments(t *testing.T) {
	segments := []sourceSegment{
		exportTestSegment(1, 0, 60, "ready", h264()),
		exportTestSegment(2, 60, 60, "missing", h264()),
	}
	plan := analyzeExportWindow(exportTestBase, exportTestBase.Add(5*time.Minute), segments)
	if len(plan.Unavailable) != 1 || plan.Unavailable[0].State != "missing" {
		t.Fatal("unavailable segment must be reported, not hidden", plan.Unavailable)
	}
	if len(plan.Gaps) == 0 {
		t.Fatal("a missing segment's span is uncovered time")
	}
}

func TestAnalyzeExportWindowMediaConsistency(t *testing.T) {
	hevc := h264()
	hevc.Codec = "hevc"
	mixed := []sourceSegment{
		exportTestSegment(1, 0, 60, "ready", h264()),
		exportTestSegment(2, 60, 60, "ready", hevc),
	}
	if analyzeExportWindow(exportTestBase, exportTestBase.Add(5*time.Minute), mixed).MediaConsistent {
		t.Fatal("mixed codecs must be flagged")
	}
	// Evidence predating the media capture is a wildcard, not a disagreement.
	legacy := []sourceSegment{
		exportTestSegment(1, 0, 60, "ready", MediaParams{}),
		exportTestSegment(2, 60, 60, "ready", h264()),
	}
	if !analyzeExportWindow(exportTestBase, exportTestBase.Add(5*time.Minute), legacy).MediaConsistent {
		t.Fatal("zero evidence must not read as a conflict")
	}
}

func TestGroupChainSplitBoundaries(t *testing.T) {
	segments := []sourceSegment{
		exportTestSegment(1, 0, 60, "ready", h264()),
		exportTestSegment(2, 60, 60, "ready", h264()),
		exportTestSegment(3, 600, 60, "ready", h264()),
	}
	groups := groupChain(segments)
	if len(groups) != 2 || len(groups[0]) != 2 || len(groups[1]) != 1 {
		t.Fatal("split boundaries wrong", groups)
	}
}

func TestExportWindowValidation(t *testing.T) {
	base := exportTestBase
	if err := validateExportWindow(base, base.Add(30*time.Minute)); err != nil {
		t.Fatal("thirty minutes is the documented maximum", err)
	}
	if err := validateExportWindow(base, base.Add(30*time.Minute+time.Second)); err != ErrExportWindowTooLong {
		t.Fatal("over the maximum must be refused", err)
	}
	if err := validateExportWindow(base.Add(time.Minute), base); err != ErrExportWindowInvalid {
		t.Fatal("reversed window must be refused", err)
	}
	if err := validateExportWindow(base, base.Add(500*time.Millisecond)); err != ErrExportWindowInvalid {
		t.Fatal("a window shorter than one second is not exportable", err)
	}
}

func TestExportChainOrder(t *testing.T) {
	plan := exportPlan{Groups: []exportGroup{
		{Segments: []sourceSegment{exportTestSegment(1, 0, 60, "ready", h264())}},
		{Segments: []sourceSegment{exportTestSegment(2, 300, 60, "ready", h264()), exportTestSegment(3, 360, 60, "ready", h264())}},
	}}
	chain := exportChain(plan)
	if len(chain) != 3 || chain[0].Start.After(chain[2].Start) {
		t.Fatal("chain must flatten groups in start order", chain)
	}
}
