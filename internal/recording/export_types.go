package recording

import (
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// ExportMaxWindow is the single-task limit from PRD EXP-01: one task exports at
// most thirty minutes of material; longer periods are split into more tasks.
const ExportMaxWindow = 30 * time.Minute

// SeamTolerance absorbs the sub-second keyframe seams between consecutive
// sixty-second segments. A hole shorter than this is a seam of continuous
// recording, not a gap worth reporting or splitting an artifact over.
const SeamTolerance = 2 * time.Second

// ExportMaxArtifacts bounds one task's split output. A thirty-minute window of
// sixty-second segments cannot legitimately exceed it; a fragmented source
// beyond this count should be exported in pieces the operator chooses.
const ExportMaxArtifacts = 64

// sourceSegment is the analysis view of one indexed segment, clipped to the
// export window by the caller.
type sourceSegment struct {
	ID         id.ID
	State      string
	Start, End time.Time
	Bytes      int64
	Media      MediaParams
}

// ExportSpan is a half-open interval on the site's absolute clock.
type ExportSpan struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// DurationMS is the API form of a span length.
func (s ExportSpan) DurationMS() int64 { return s.End.Sub(s.Start).Milliseconds() }

// exportGroup is one contiguous chain of ready segments. Split mode writes one
// artifact per group; merge mode concatenates every group in order.
type exportGroup struct {
	Start, End time.Time
	Segments   []sourceSegment
	Bytes      int64
}

func (g exportGroup) duration() time.Duration { return g.End.Sub(g.Start) }

// exportPlan is what analysis produces and both the preview endpoint and the
// task creation consume. Overlapping segments (a source switched mid-run) enter
// exactly one chain: the greedy earliest-start chain, so concatenation never
// duplicates material. Skipped duplicates are counted, not hidden.
type exportPlan struct {
	Groups          []exportGroup
	Gaps            []ExportSpan
	Unavailable     []sourceSegment
	DuplicateCount  int
	ReadyBytes      int64
	MediaConsistent bool
}

func (p exportPlan) coverage() time.Duration {
	total := time.Duration(0)
	for _, group := range p.Groups {
		total += group.duration()
	}
	return total
}

// analyzeExportWindow merges the ready segments inside the window into chains
// and derives the gap complement. It is pure so the preview a client sees is
// byte-for-byte the plan a task later executes.
func analyzeExportWindow(from, to time.Time, segments []sourceSegment) exportPlan {
	plan := exportPlan{MediaConsistent: true}
	var ready []sourceSegment
	for _, segment := range segments {
		if segment.End.Before(segment.Start) {
			continue
		}
		if segment.Start.Before(from) {
			segment.Start = from
		}
		if segment.End.After(to) {
			segment.End = to
		}
		if !segment.End.After(segment.Start) {
			continue
		}
		if segment.State == "ready" {
			ready = append(ready, segment)
		} else {
			plan.Unavailable = append(plan.Unavailable, segment)
		}
	}
	plan.MediaConsistent = mediaConsistent(ready)
	var chain *exportGroup
	closeChain := func() {
		if chain != nil {
			plan.Groups = append(plan.Groups, *chain)
			chain = nil
		}
	}
	for _, segment := range ready {
		switch {
		case chain == nil:
			chain = &exportGroup{Start: segment.Start, End: segment.End, Segments: []sourceSegment{segment}, Bytes: segment.Bytes}
		case segment.Start.Before(chain.End):
			// Overlap with material already in the chain: keep the first copy
			// (earliest start) so a source switch never doubles content.
			plan.DuplicateCount++
		case segment.Start.After(chain.End.Add(SeamTolerance)):
			closeChain()
			chain = &exportGroup{Start: segment.Start, End: segment.End, Segments: []sourceSegment{segment}, Bytes: segment.Bytes}
		default:
			chain.End = segment.End
			chain.Segments = append(chain.Segments, segment)
			chain.Bytes += segment.Bytes
		}
	}
	closeChain()
	for _, group := range plan.Groups {
		plan.ReadyBytes += group.Bytes
	}
	plan.Gaps = complementSpans(from, to, plan.Groups)
	return plan
}

// complementSpans lists the uncovered stretches of [from,to) around the group
// coverage, including the leading and trailing regions.
func complementSpans(from, to time.Time, groups []exportGroup) []ExportSpan {
	if !to.After(from) {
		return nil
	}
	var gaps []ExportSpan
	cursor := from
	for _, group := range groups {
		if group.Start.After(cursor) {
			gaps = append(gaps, ExportSpan{Start: cursor, End: group.Start})
		}
		if group.End.After(cursor) {
			cursor = group.End
		}
	}
	if cursor.Before(to) {
		gaps = append(gaps, ExportSpan{Start: cursor, End: to})
	}
	return gaps
}

// mediaConsistent compares the recorded publication evidence of the chain:
// segments whose evidence predates the capture are wildcards, any two that do
// state parameters must agree. A disagreement would make -c copy produce a
// broken file, which is why creation warns and the task refuses (EXP-02).
func mediaConsistent(segments []sourceSegment) bool {
	var reference MediaParams
	for _, segment := range segments {
		if segment.Media.Codec == "" {
			continue
		}
		if reference.Codec == "" {
			reference = segment.Media
			continue
		}
		if segment.Media.Codec != reference.Codec || segment.Media.Width != reference.Width || segment.Media.Height != reference.Height || segment.Media.FPS != reference.FPS {
			return false
		}
	}
	return true
}
