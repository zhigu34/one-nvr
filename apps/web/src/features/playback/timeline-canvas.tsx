import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type WheelEvent as ReactWheelEvent,
} from 'react'
import { useTheme } from '@/context/theme-provider'
import { formatClock } from './zone'
import {
  entryAtAny,
  mergeEntries,
  snapToPlayable,
  type Entry,
  type Timeline as TimelineData,
} from './timeline'
import { clusterMarks, type EventMark } from './event-marks'

/**
 * The interactive timeline strip, in three stacked tracks:
 *
 *   1. a precision time ruler (major ticks on the hour, minor on the half and
 *      quarter hour) so any position can be read without hovering;
 *   2. the recording track — translucent fills with a bright top edge, which
 *      leaves the contrast budget for the layer above;
 *   3. the AI event track, drawn ON the recording track at its true time span
 *      and colour-coded by category.
 *
 * One `<canvas>` draws the dense layers; a thin DOM layer owns only what needs
 * events or per-frame updates — the playhead, the export bracket, the hover
 * cursor and its tooltip.
 *
 * Why canvas: a 7-day window is thousands of one-minute segments plus hundreds
 * of event marks. Drawing them as DOM nodes reflows on every zoom and shows
 * sub-pixel seams between adjacent slices; a canvas repaint is one raster pass,
 * and the 1.5 s merge in `mergeEntries` keeps adjacent segments seamless so only
 * true dropouts read as gaps.
 */

// Track geometry. The ruler is a fixed height because its labels have to line up
// with the ticks; the two data tracks share the rest.
// Heights chosen so each layer is legible on its own: the ruler needs room for
// two lines at midnight (time + date), the recording track needs enough height
// for its translucent fill to read as a span rather than a line, and the event
// track must be tall enough that a 3px-wide mark still looks like a block.
const RULER_H = 30
const RECORDING_H = 44
const EVENTS_H = 26
const GAP_H = 4
const HEIGHT = RULER_H + GAP_H + RECORDING_H + GAP_H + EVENTS_H

const RULER_TOP = 0
const RECORDING_TOP = RULER_H + GAP_H
const EVENTS_TOP = RECORDING_TOP + RECORDING_H + GAP_H

// Every event mark is at least this wide, whatever its true duration. At a
// 24-hour view a 12-second detection is 0.3px — invisible and unclickable, so
// the marks would silently vanish exactly when the operator is scanning for
// them. 3px is the narrowest block that still reads as a block.
const MARK_MIN_W = 3

const RECORDING_FILL = 'rgba(16, 185, 129, 0.22)'
const RECORDING_EDGE = '#10b981'
const BLOCKED_FILL = 'rgba(239, 68, 68, 0.28)'
const BLOCKED_EDGE = '#ef4444'
const EVENT_PERSON = '#f59e0b'
const EVENT_VEHICLE = '#38bdf8'
const EVENT_OTHER = '#c084fc'

/** Event categories that get the vehicle colour rather than the neutral one. */
const VEHICLE_CATEGORIES = new Set([
  'car',
  'truck',
  'bus',
  'motorcycle',
  'bicycle',
])

/**
 * Which colour a mark is drawn in. Person events are the ones an operator
 * scans for, so they take the warm accent; vehicles are the second most common
 * and get their own hue; everything else falls back to a neutral third rather
 * than borrowing a colour that already means something else.
 */
function markColor(category: string): string {
  if (category === 'person') return EVENT_PERSON
  if (VEHICLE_CATEGORIES.has(category)) return EVENT_VEHICLE
  return EVENT_OTHER
}

// The tracks are a dark industrial surface in both themes; only the ink on the
// ruler and the empty-track texture change, because a pale strip beside a black
// video looks like a different application.
const CANVAS_PALETTES = {
  dark: {
    surface: '#0b0f14',
    border: 'rgba(255,255,255,0.10)',
    emptyTrack: 'rgba(255,255,255,0.025)',
    emptyHatch: 'rgba(255,255,255,0.04)',
    tickMajor: 'rgba(255,255,255,0.55)',
    tickMinor: 'rgba(255,255,255,0.22)',
    labelMajor: 'rgba(255,255,255,0.92)',
    labelMinor: 'rgba(255,255,255,0.38)',
  },
  light: {
    surface: '#0b0f14',
    border: 'rgba(255,255,255,0.10)',
    emptyTrack: 'rgba(255,255,255,0.025)',
    emptyHatch: 'rgba(255,255,255,0.04)',
    tickMajor: 'rgba(255,255,255,0.55)',
    tickMinor: 'rgba(255,255,255,0.22)',
    labelMajor: 'rgba(255,255,255,0.92)',
    labelMinor: 'rgba(255,255,255,0.38)',
  },
} as const

export type TimelineCanvasProps = {
  timeline: TimelineData
  zone: string
  clock: number
  selected: Entry | null
  disabled: boolean
  marks: (EventMark & { count?: number })[]
  bracket: { start: number; end: number } | null
  pickMode: boolean
  viewIsFull: boolean
  onPick: (at: number) => void
  onSetBracket: (bracket: { start: number; end: number } | null) => void
  onSelect: (entry: Entry) => void
  onSeek: (at: number) => void
  onPlay: (entry: Entry) => void
  onZoomAt: (anchor: number, factor: number) => void
  onMoveView: (center: number) => void
}

function xFor(at: number, from: number, span: number, width: number) {
  if (span <= 0 || width <= 0) return 0
  return ((at - from) / span) * width
}

function timeFor(px: number, from: number, span: number, width: number) {
  if (width <= 0) return from
  return from + (px / width) * span
}

/**
 * The mark whose drawn block covers `px`, or null.
 *
 * The hit area is the DRAWN width, not the true span: a 12-second event is
 * rendered 3px wide at a 24-hour view, and hit-testing its real width would make
 * the visible block unclickable — the one thing the minimum-width rule exists to
 * prevent.
 */
function markAt(
  marks: (EventMark & { count?: number })[],
  px: number,
  from: number,
  span: number,
  width: number
): (EventMark & { count?: number }) | null {
  let best: (EventMark & { count?: number }) | null = null
  for (const mark of marks) {
    const x = xFor(mark.at, from, span, width)
    const drawnW = Math.max(xFor(mark.end, from, span, width) - x, MARK_MIN_W)
    // A pointer sitting ON a 3px block must find it: the tolerance covers the
    // block plus a pixel either side, which is the whole drawn footprint.
    if (px >= x - 1 && px <= x + drawnW + 1) {
      // Overlapping marks: prefer the later one so the newest event wins a
      // tie, which is the one an operator is usually chasing.
      if (!best || mark.at >= best.at) best = mark
    }
  }
  return best
}

export function TimelineCanvas({
  timeline,
  zone,
  clock,
  selected,
  disabled,
  marks,
  bracket,
  pickMode,
  viewIsFull,
  onPick,
  onSetBracket,
  onSelect,
  onSeek,
  onPlay,
  onZoomAt,
  onMoveView,
}: TimelineCanvasProps) {
  const { resolvedTheme } = useTheme()
  const palette = CANVAS_PALETTES[resolvedTheme === 'light' ? 'light' : 'dark']
  const wrapRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [width, setWidth] = useState(0)
  const [dragging, setDragging] = useState(false)
  // Mirrors scrubbingRef for the cursor only: a ref cannot be read during
  // render, and the cursor is render output.
  const [scrubbing, setScrubbing] = useState(false)
  const [hover, setHover] = useState<{
    px: number
    at: number
    mark: (EventMark & { count?: number }) | null
  } | null>(null)
  const handleRef = useRef<'start' | 'end' | null>(null)
  const pointerDownRef = useRef<{ x: number; at: number; moved: boolean } | null>(null)
  const scrubbingRef = useRef(false)

  const from = timeline.from
  const span = timeline.to - timeline.from
  const merged = useMemo(
    () => mergeEntries(timeline.entries),
    [timeline.entries]
  )
  // Clustered against the DRAWN width: 200 events across 24 hours land within a
  // few pixels of each other, and drawing them individually produces one solid
  // smear. Clustering keeps "there were events here" readable and reports the
  // count on hover; it re-folds whenever the width or the view changes, so
  // zooming in unfolds the cluster.
  // 10px, not 5: at 5px the fold was finer than a pointer, so a dense stretch
  // became a row of 3px islands with gaps between them — hoverable in theory and
  // impossible in practice. 10px is about the smallest block an operator can
  // reliably aim at.
  const drawnMarks = useMemo(
    () => clusterMarks(marks, from, timeline.to, width, 10),
    [marks, from, timeline.to, width]
  )

  // Track container width — the canvas raster and the DOM overlays both need it.
  useEffect(() => {
    const element = wrapRef.current
    if (!element) return
    const update = () => setWidth(element.clientWidth)
    update()
    const observer = new ResizeObserver(update)
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  // One raster pass over all three tracks. Dependencies are the draw inputs
  // only; playhead movement does NOT repaint (the DOM cursor handles that).
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas || width <= 0) return
    const dpr = window.devicePixelRatio || 1
    canvas.width = Math.round(width * dpr)
    canvas.height = Math.round(HEIGHT * dpr)
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, width, HEIGHT)

    // --- Track surfaces: one dark plate per track, hairline border, so the
    // three layers read as stacked instruments rather than one gradient.
    ctx.fillStyle = palette.surface
    ctx.fillRect(0, RULER_TOP, width, RULER_H)
    ctx.fillRect(0, RECORDING_TOP, width, RECORDING_H)
    ctx.fillRect(0, EVENTS_TOP, width, EVENTS_H)
    ctx.strokeStyle = palette.border
    ctx.lineWidth = 1
    for (const top of [RULER_TOP, RECORDING_TOP, EVENTS_TOP]) {
      const h = top === RULER_TOP ? RULER_H : top === RECORDING_TOP ? RECORDING_H : EVENTS_H
      ctx.strokeRect(0.5, top + 0.5, width - 1, h - 1)
    }

    // Empty-track texture: a faint hatch tells "nothing recorded here" apart
    // from "not drawn", which a flat fill cannot do on a dark plate.
    ctx.save()
    ctx.beginPath()
    ctx.rect(0, RECORDING_TOP + 1, width, RECORDING_H - 1)
    ctx.clip()
    ctx.strokeStyle = palette.emptyHatch
    ctx.lineWidth = 1
    for (let x = -RECORDING_H; x < width + RECORDING_H; x += 6) {
      ctx.beginPath()
      ctx.moveTo(x, RECORDING_TOP + RECORDING_H)
      ctx.lineTo(x + RECORDING_H, RECORDING_TOP)
      ctx.stroke()
    }
    ctx.restore()

    // --- Ruler: major ticks on the hour (tall, labelled), minor on the half
    // and quarter hour. The ladder is chosen from the visible span so a 24-hour
    // view labels every two hours while a 1-hour view labels every ten minutes.
    const spanSeconds = span / 1000
    const majorLadder = [60, 300, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400]
    const labelTarget = Math.max(4, Math.floor(width / 96))
    let majorStep = majorLadder[majorLadder.length - 1]
    for (const candidate of majorLadder) {
      if (spanSeconds / candidate <= labelTarget) {
        majorStep = candidate
        break
      }
    }
    const minorStep = majorStep / 4

    // Minor ticks first so a major tick overdraws its neighbour cleanly.
    ctx.strokeStyle = palette.tickMinor
    ctx.beginPath()
    const firstMinor = Math.ceil(from / (minorStep * 1000)) * minorStep * 1000
    for (let at = firstMinor; at <= timeline.to; at += minorStep * 1000) {
      const x = Math.round(xFor(at, from, span, width)) + 0.5
      ctx.moveTo(x, RULER_H - 5)
      ctx.lineTo(x, RULER_H - 1)
    }
    ctx.stroke()

    ctx.font = '10px ui-monospace, SFMono-Regular, Menlo, monospace'
    ctx.textBaseline = 'top'
    const firstMajor = Math.ceil(from / (majorStep * 1000)) * majorStep * 1000
    for (let at = firstMajor; at <= timeline.to; at += majorStep * 1000) {
      const x = Math.round(xFor(at, from, span, width)) + 0.5
      ctx.strokeStyle = palette.tickMajor
      ctx.beginPath()
      ctx.moveTo(x, RULER_H - 9)
      ctx.lineTo(x, RULER_H - 1)
      ctx.stroke()
      // Labels sit at the top of the ruler, inset from the edges so the first
      // and last are never clipped by the plate border.
      //
      // formatClock returns "YYYY-MM-DD HH:mm:ss", so the time is characters
      // 11-16 — slicing from 0 printed the date, and every label read "2026-".
      const clock = formatClock(at, zone)
      const label = clock.slice(11, 16)
      // Midnight is the one label that needs the date: without it a 24-hour
      // view has two "00:00" ticks and no way to tell which day is which.
      const dayLabel = clock.slice(0, 10)
      ctx.fillStyle = palette.labelMajor
      const edge = x < 22 ? 'left' : x > width - 22 ? 'right' : 'center'
      ctx.textAlign = edge
      const textX = edge === 'left' ? 3 : edge === 'right' ? width - 3 : x
      // Midnight stacks the date under the time. The second line sits lower than
      // a single line would so the two never overlap the tick marks that start
      // 9px above the ruler's bottom edge.
      ctx.fillText(label, textX, 2)
      if (label === '00:00') {
        ctx.fillStyle = palette.labelMinor
        ctx.fillText(dayLabel, textX, 14)
      }
    }

    // --- Recording track: translucent fill with a bright top edge. The fill is
    // deliberately weak so the event layer above it stays legible where they
    // overlap; the edge is what makes a span readable at a glance.
    const drawSpan = (
      start: number,
      end: number,
      fill: string,
      edge: string
    ) => {
      const x = xFor(start, from, span, width)
      const w = Math.max(xFor(end, from, span, width) - x, 1)
      ctx.fillStyle = fill
      ctx.fillRect(x, RECORDING_TOP + 1, w, RECORDING_H - 1)
      ctx.fillStyle = edge
      ctx.fillRect(x, RECORDING_TOP + 1, w, 2)
    }
    for (const piece of merged.blocked) {
      drawSpan(piece.start, piece.end, BLOCKED_FILL, BLOCKED_EDGE)
    }
    for (const piece of merged.playable) {
      drawSpan(piece.start, piece.end, RECORDING_FILL, RECORDING_EDGE)
    }
    if (selected) {
      const x = xFor(selected.start, from, span, width)
      const w = Math.max(xFor(selected.end, from, span, width) - x, 2)
      ctx.strokeStyle = '#fbbf24'
      ctx.lineWidth = 1.5
      ctx.strokeRect(x + 0.5, RECORDING_TOP + 1.5, w - 1, RECORDING_H - 3)
    }

    // --- Event track: one block per mark at its true span, floored to
    // MARK_MIN_W. Every mark is drawn the same way: this track answers "when did
    // something happen", and handle state is a worklist concern that belongs on
    // the events page, not on a time axis.
    for (const mark of drawnMarks) {
      const x = xFor(mark.at, from, span, width)
      const w = Math.max(xFor(mark.end, from, span, width) - x, MARK_MIN_W)
      ctx.fillStyle = markColor(mark.category)
      ctx.fillRect(x, EVENTS_TOP + 3, w, EVENTS_H - 7)
      // A cap dot marks the exact start when the block is wider than the floor:
      // the span is [start-5s, end+5s], and the dot is the event itself.
      if (w > 6) {
        ctx.fillStyle = '#ffffff'
        ctx.fillRect(x, EVENTS_TOP + 3, 1, EVENTS_H - 7)
      }
    }
  }, [width, merged, drawnMarks, timeline.from, timeline.to, from, span, selected, viewIsFull, zone, palette])

  const playheadAt = clock > 0 ? clock : selected?.start
  const playheadX =
    playheadAt != null && span > 0 ? xFor(playheadAt, from, span, width) : null

  const pointerAt = (event: { clientX: number }) => {
    const box = wrapRef.current?.getBoundingClientRect()
    if (!box) return null
    const px = event.clientX - box.left
    return { px, at: Math.round(timeFor(px, from, span, box.width)), box }
  }

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (disabled) return
    const hit = pointerAt(event)
    if (!hit) return
    event.currentTarget.setPointerCapture(event.pointerId)
    // A press on the ruler or the playhead scrubs: the operator is aiming at a
    // time, not at a block. A press on a block still selects it (below).
    // Scrubbing is for aiming at a TIME. In pick mode the operator is aiming at
    // a RANGE, so a press must not also move the playhead — that made the
    // bracket's first click jump playback before the range existed.
    const onRuler = event.nativeEvent.offsetY <= RULER_H
    if (onRuler && !pickMode) {
      scrubbingRef.current = true
      setScrubbing(true)
      onSeek(hit.at)
    }
    pointerDownRef.current = { x: hit.px, at: hit.at, moved: false }
    if (bracket && pickMode) {
      const startPx = xFor(bracket.start, from, span, width)
      const endPx = xFor(bracket.end, from, span, width)
      if (Math.abs(hit.px - startPx) <= 6) handleRef.current = 'start'
      else if (Math.abs(hit.px - endPx) <= 6) handleRef.current = 'end'
    }
  }

  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const hit = pointerAt(event)
    if (!hit || span <= 0) return
    const { px, at, box } = hit
    const mark = markAt(drawnMarks, px, from, span, box.width)
    setHover({ px, at, mark })

    if (scrubbingRef.current && !pickMode) {
      onSeek(at)
      return
    }
    if (handleRef.current && bracket) {
      const edge = handleRef.current
      onSetBracket({
        start: edge === 'start' ? Math.min(at, bracket.end - 1000) : bracket.start,
        end: edge === 'end' ? Math.max(at, bracket.start + 1000) : bracket.end,
      })
      return
    }
    const down = pointerDownRef.current
    if (down) {
      const dx = px - down.x
      if (!down.moved && Math.abs(dx) > 4) down.moved = true
      if (down.moved && !pickMode) {
        if (!dragging) setDragging(true)
        const movedAt = Math.round(timeFor(down.x, from, span, box.width))
        onMoveView(movedAt - (dx / box.width) * span)
        return
      }
    }
  }

  const onPointerUp = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (disabled) return
    const wasScrubbing = scrubbingRef.current
    scrubbingRef.current = false
    setScrubbing(false)
    handleRef.current = null
    const down = pointerDownRef.current
    pointerDownRef.current = null
    setDragging(false)
    if (!down) return
    const hit = pointerAt(event)
    if (!hit) return
    if (wasScrubbing && !down.moved) return
    if (!down.moved) {
      if (pickMode) {
        onPick(hit.at)
        return
      }
      const mark = markAt(drawnMarks, hit.px, from, span, hit.box.width)
      if (mark) {
        // Clicking an event jumps to the clip that covers its lead-in — the
        // span is [start-5s, end+5s], so playing from its start is what shows
        // the seconds before the detection.
        onSeek(snapToPlayable(timeline, mark.at).at)
        return
      }
      const entry = entryAtAny(timeline, hit.at)
      if (entry) {
        onSelect(entry)
        return
      }
      onSeek(snapToPlayable(timeline, hit.at).at)
    }
  }

  const onDoubleClick = (event: ReactMouseEvent<HTMLDivElement>) => {
    if (disabled || pickMode) return
    const hit = pointerAt(event)
    if (!hit) return
    const entry = entryAtAny(timeline, hit.at)
    if (entry) onPlay(entry)
    else onSeek(snapToPlayable(timeline, hit.at).at)
  }

  const onWheelZoom = (event: ReactWheelEvent<HTMLDivElement>) => {
    if (disabled) return
    event.preventDefault()
    const hit = pointerAt(event)
    if (!hit) return
    if (event.shiftKey) {
      onMoveView(hit.at + (event.deltaY > 0 ? 1 : -1) * span * 0.25)
    } else {
      onZoomAt(hit.at, event.deltaY > 0 ? 1.4 : 1 / 1.4)
    }
  }

  const bracketPx =
    bracket && bracket.end > bracket.start && span > 0
      ? {
          left: xFor(bracket.start, from, span, width),
          right: xFor(bracket.end, from, span, width),
        }
      : null

  return (
    <div
      ref={wrapRef}
      data-testid='playback-timeline-canvas'
      aria-label='录像时间轴'
      role='application'
      className={`relative touch-none select-none overflow-hidden rounded-md border border-video-border ${disabled ? 'cursor-not-allowed' : dragging ? 'cursor-grabbing' : scrubbing ? 'cursor-ew-resize' : 'cursor-crosshair'}`}
      style={{ height: HEIGHT }}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerLeave={() => {
        setHover(null)
        scrubbingRef.current = false
        setScrubbing(false)
      }}
      onDoubleClick={onDoubleClick}
      onWheel={onWheelZoom}
    >
      <canvas
        ref={canvasRef}
        style={{ width: '100%', height: HEIGHT }}
        className='pointer-events-none block'
      />

      {/* Hover cursor: a dashed vertical line through all three tracks plus the
          time readout. It answers "where am I" without the operator having to
          press anything, which the playhead alone cannot do. */}
      {hover && !dragging && (
        <>
          <div
            aria-hidden
            className='pointer-events-none absolute top-0 z-10 w-px border-l border-dashed border-white/35'
            style={{ left: hover.px, height: HEIGHT }}
          />
          <div
            role='tooltip'
            data-testid='playback-hover-tooltip'
            className='pointer-events-none absolute z-20 whitespace-nowrap rounded border border-white/15 bg-popover px-1.5 py-0.5 text-[10px] tabular-nums text-popover-foreground shadow-md'
            style={{
              left: Math.min(Math.max(hover.px, 70), Math.max(width - 70, 70)),
              top: 1,
              transform: 'translateX(-50%)',
            }}
          >
            {hover.mark ? (
              <span className='flex items-center gap-1.5'>
                <span
                  className='inline-block size-1.5 rounded-full'
                  style={{ background: markColor(hover.mark.category) }}
                />
                <span className='font-medium'>{hover.mark.category || '事件'}</span>
                <span className='text-muted-foreground'>
                  {formatClock(hover.mark.at, zone)}
                </span>
                {hover.mark.count && hover.mark.count > 1 ? (
                  <span className='text-muted-foreground'>
                    {hover.mark.count} 个事件
                  </span>
                ) : (
                  <span className='text-muted-foreground'>
                    持续 {Math.max(1, Math.round((hover.mark.end - hover.mark.at) / 1000))}s
                  </span>
                )}
              </span>
            ) : (
              formatClock(hover.at, zone)
            )}
          </div>
        </>
      )}

      {/* Export bracket: translucent mask over the recording track only. */}
      {bracketPx && (
        <div
          data-testid='playback-bracket'
          aria-label='导出区间'
          className='pointer-events-none absolute'
          style={{
            left: bracketPx.left,
            width: bracketPx.right - bracketPx.left,
            top: RECORDING_TOP,
            height: RECORDING_H,
          }}
        >
          <div
            className='absolute inset-0'
            style={{
              background:
                'repeating-linear-gradient(45deg, rgba(251,191,36,0.25) 0 6px, transparent 6px 12px)',
            }}
          />
          <div className='absolute -left-1.5 top-0 h-full w-3 cursor-col-resize' data-handle='start' />
          <div className='absolute -right-1.5 top-0 h-full w-3 cursor-col-resize' data-handle='end' />
        </div>
      )}

      {/* Playhead: one 2px line through all three tracks with a capsule label
          showing the time it sits on. The capsule is the only place the exact
          position is stated, so it must not clip at either edge. */}
      {playheadX !== null && (
        <div
          data-testid='playback-playhead'
          className='pointer-events-none absolute top-0 z-30'
          style={{ left: 0, height: HEIGHT, width: 2 }}
        >
          <div
            className='absolute inset-y-0 w-0.5 bg-red-500 shadow-[0_0_8px_rgba(239,68,68,0.75)]'
            style={{ transform: `translateX(${playheadX - 1}px)` }}
          />
          <div
            className='absolute flex items-center gap-1 rounded-sm bg-red-500 px-1 py-px text-[10px] font-medium leading-4 tabular-nums text-white shadow-sm'
            style={{
              transform: `translateX(${Math.min(Math.max(playheadX - 26, 0), Math.max(width - 52, 0))}px)`,
              top: HEIGHT - 18,
            }}
          >
            {playheadAt ? formatClock(playheadAt, zone) : '—'}
          </div>
        </div>
      )}
    </div>
  )
}
