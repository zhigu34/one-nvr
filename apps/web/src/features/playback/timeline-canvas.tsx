import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type WheelEvent as ReactWheelEvent,
} from 'react'
import { formatClock } from './zone'
import {
  entryAtAny,
  mergeEntries,
  snapToPlayable,
  type Entry,
  type Timeline as TimelineData,
} from './timeline'

/**
 * The interactive timeline strip: one `<canvas>` draws the dense layers
 * (coverage bars, tick grid, event markers) and a thin DOM layer on top owns
 * only the things that need events or per-frame updates — the playhead, the
 * export bracket and the hover tooltip.
 *
 * Why canvas: a 7-day window is thousands of one-minute segments plus
 * hundreds of event marks. Drawing them as DOM nodes reflows on every zoom
 * and shows sub-pixel seams between adjacent slices; a canvas repaint is one
 * raster pass, and the 1.5 s merge in `mergeEntries` keeps adjacent segments
 * seamless so only true dropouts read as gaps.
 */

const HEIGHT = 76
const DENSITY_H = 8
const TICKS_H = 12
const SEGMENTS_H = 30
const EVENTS_H = 12
// Ticks sit between the density bar and the segment bar; 1px of headroom on
// both sides keeps the density inset from the rounded frame. The event track
// keeps a 4px bottom inset so amber marks never touch the canvas edge.
const DENSITY_TOP = 1
const TICKS_TOP = DENSITY_TOP + DENSITY_H
const SEGMENTS_TOP = TICKS_TOP + TICKS_H
const EVENTS_TOP = SEGMENTS_TOP + SEGMENTS_H
const EVENT_BOTTOM_INSET = 4

const PLAYABLE = '#10b981'
const BLOCKED = '#ef4444'
const EVENT = '#f59e0b'
const EVENT_OPEN = '#fbbf24'

export type TimelineCanvasProps = {
  timeline: TimelineData
  zone: string
  clock: number
  selected: Entry | null
  disabled: boolean
  marks: ReturnType<typeof import('./event-marks').eventMarks>
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

function roundRect(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  w: number,
  h: number,
  r: number
) {
  const radius = Math.min(r, w / 2, h / 2)
  ctx.beginPath()
  ctx.moveTo(x + radius, y)
  ctx.arcTo(x + w, y, x + w, y + h, radius)
  ctx.arcTo(x + w, y + h, x, y + h, radius)
  ctx.arcTo(x, y + h, x, y, radius)
  ctx.arcTo(x, y, x + w, y, radius)
  ctx.closePath()
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
  const wrapRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const tooltipRef = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(0)
  const [dragging, setDragging] = useState(false)
  // Bracket handle drag: which edge of an existing bracket is being pulled.
  const handleRef = useRef<'start' | 'end' | null>(null)
  const pointerDownRef = useRef<{ x: number; at: number; moved: boolean } | null>(null)

  const from = timeline.from
  const span = timeline.to - timeline.from
  const merged = useMemo(
    () => mergeEntries(timeline.entries),
    [timeline.entries]
  )

  // Track container width — the canvas raster and the DOM cursor both need it.
  useEffect(() => {
    const element = wrapRef.current
    if (!element) return
    const update = () => setWidth(element.clientWidth)
    update()
    const observer = new ResizeObserver(update)
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  // One raster pass over all layers. Dependencies are the draw inputs only;
  // playhead movement does NOT repaint (the DOM cursor handles that).
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

    // --- Density band: the whole searched window's coverage, merged spans
    // drawn as one continuous bar so this row reads "how much exists".
    ctx.fillStyle = 'rgba(255,255,255,0.05)'
    roundRect(ctx, 0, DENSITY_TOP, width, DENSITY_H, 3)
    ctx.fill()
    ctx.fillStyle = 'rgba(16,185,129,0.55)'
    for (const piece of merged.playable) {
      const x = xFor(piece.start, from, span, width)
      const w = Math.max(xFor(piece.end, from, span, width) - x, 1)
      ctx.fillRect(x, DENSITY_TOP, w, DENSITY_H)
    }
    // Zoom viewport outline.
    if (!viewIsFull) {
      ctx.strokeStyle = '#f59e0b'
      ctx.lineWidth = 1.5
      const vx = xFor(timeline.from, from, span, width)
      const vw = Math.max(xFor(timeline.to, from, span, width) - vx, 4)
      ctx.strokeRect(vx + 0.5, DENSITY_TOP + 0.5, vw - 1, DENSITY_H - 1)
    }

    // --- Tick grid + labels across the segment band height, self-scaling
    // label step from the view span.
    ctx.font = '10px ui-monospace, SFMono-Regular, Menlo, monospace'
    ctx.textBaseline = 'top'
    const spanSeconds = span / 1000
    const LADDER = [
      60, 300, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400, 172800,
      604800,
    ]
    const target = Math.max(4, Math.floor(width / 110))
    let step = LADDER[LADDER.length - 1]
    for (const candidate of LADDER) {
      if (spanSeconds / candidate <= target) {
        step = candidate
        break
      }
    }
    const firstTick = Math.ceil(from / (step * 1000)) * step * 1000
    for (let at = firstTick; at <= timeline.to; at += step * 1000) {
      const x = xFor(at, from, span, width)
      const isDay = new Date(at).getUTCHours() === 0
      ctx.strokeStyle = isDay
        ? 'rgba(253,230,138,0.5)'
        : 'rgba(253,230,138,0.25)'
      ctx.lineWidth = 1
      ctx.beginPath()
      ctx.moveTo(x + 0.5, TICKS_TOP)
      ctx.lineTo(x + 0.5, TICKS_TOP + TICKS_H + SEGMENTS_H + EVENTS_H)
      ctx.stroke()
      const label = formatClock(at, zone).slice(isDay ? 5 : 11)
      ctx.fillStyle = isDay
        ? 'rgba(255,255,255,0.9)'
        : 'rgba(255,255,255,0.5)'
      const edge = x < 18 ? 'left' : x > width - 18 ? 'right' : 'center'
      ctx.textAlign = edge
      ctx.fillText(label, edge === 'left' ? 2 : edge === 'right' ? width - 2 : x, TICKS_TOP + 1)
    }

    // --- Segment band: merged playable (green) and blocked (red) spans.
    for (const piece of merged.blocked) {
      const x = xFor(piece.start, from, span, width)
      const w = Math.max(xFor(piece.end, from, span, width) - x, 1)
      ctx.fillStyle = BLOCKED
      roundRect(ctx, x, SEGMENTS_TOP, w, SEGMENTS_H, 3)
      ctx.fill()
    }
    for (const piece of merged.playable) {
      const x = xFor(piece.start, from, span, width)
      const w = Math.max(xFor(piece.end, from, span, width) - x, 1)
      ctx.fillStyle = PLAYABLE
      roundRect(ctx, x, SEGMENTS_TOP, w, SEGMENTS_H, 3)
      ctx.fill()
    }
    // Selected segment gets a bright outline drawn over the merged bars.
    if (selected) {
      const x = xFor(selected.start, from, span, width)
      const w = Math.max(xFor(selected.end, from, span, width) - x, 2)
      ctx.strokeStyle = '#fbbf24'
      ctx.lineWidth = 2
      ctx.strokeRect(x + 1, SEGMENTS_TOP + 1, w - 2, SEGMENTS_H - 2)
    }

    // --- Event band: one rounded tick per mark; open events brighter. The
    // 4px bottom inset keeps marks off the canvas edge.
    for (const mark of marks) {
      const x = xFor(mark.at, from, span, width)
      const w = Math.max(xFor(mark.end, from, span, width) - x, 2)
      ctx.fillStyle = mark.handleState === 'open' ? EVENT_OPEN : EVENT
      roundRect(
        ctx,
        x,
        EVENTS_TOP + 2,
        w,
        Math.max(EVENTS_H - 2 - EVENT_BOTTOM_INSET, 2),
        2
      )
      ctx.fill()
    }
  }, [width, merged, marks, timeline.from, timeline.to, from, span, selected, viewIsFull, zone])

  // Playhead position: derived, so the DOM cursor moves without a canvas
  // repaint and without layout thrash (transform only). Before the first
  // playback the cursor parks at the selected segment's start — the operator
  // always sees where 播放 would begin.
  const playheadAt = clock > 0 ? clock : selected?.start
  const playheadX =
    playheadAt != null && span > 0 ? xFor(playheadAt, from, span, width) : null

  const pointerAt = (event: { clientX: number }) => {
    const box = wrapRef.current?.getBoundingClientRect()
    if (!box) return null
    return {
      px: event.clientX - box.left,
      at: Math.round(timeFor(event.clientX - box.left, from, span, box.width)),
    }
  }

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (disabled) return
    const hit = pointerAt(event)
    if (!hit) return
    event.currentTarget.setPointerCapture(event.pointerId)
    pointerDownRef.current = { x: hit.px, at: hit.at, moved: false }
    // Bracket handle grab takes priority over pan.
    if (bracket && pickMode) {
      const startPx = xFor(bracket.start, from, span, width)
      const endPx = xFor(bracket.end, from, span, width)
      if (Math.abs(hit.px - startPx) <= 6) handleRef.current = 'start'
      else if (Math.abs(hit.px - endPx) <= 6) handleRef.current = 'end'
    }
  }

  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const box = wrapRef.current?.getBoundingClientRect()
    if (!box || span <= 0) return
    const px = event.clientX - box.left
    const at = Math.round(timeFor(px, from, span, box.width))
    // Tooltip: the time under the cursor, or the event under it.
    const tooltip = tooltipRef.current
    if (tooltip && !dragging) {
      const hitMark = marks.find(
        (mark) => px >= xFor(mark.at, from, span, box.width) - 3 &&
          px <= xFor(mark.end, from, span, box.width) + 3
      )
      const text = hitMark
        ? `${hitMark.category || '事件'} · ${Math.round(hitMark.score * 100)}% · ${formatClock(hitMark.at, zone)}`
        : formatClock(at, zone)
      tooltip.textContent = text
      tooltip.style.transform = `translateX(${Math.min(Math.max(px, 60), box.width - 60)}px)`
      tooltip.style.visibility = 'visible'
    }
    // Bracket handle drag.
    if (handleRef.current && bracket) {
      const edge = handleRef.current
      onSetBracket({
        start: edge === 'start' ? Math.min(at, bracket.end - 1000) : bracket.start,
        end: edge === 'end' ? Math.max(at, bracket.start + 1000) : bracket.end,
      })
      return
    }
    // Pan once the pointer moves past the click slop.
    const down = pointerDownRef.current
    if (down) {
      const dx = px - down.x
      if (!down.moved && Math.abs(dx) > 4) down.moved = true
      if (down.moved && !pickMode) {
        if (!dragging) setDragging(true)
        const movedAt = Math.round(timeFor(down.x, from, span, box.width))
        onMoveView(movedAt - dx / box.width * span)
        return
      }
    }
  }

  const onPointerUp = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (disabled) return
    handleRef.current = null
    const down = pointerDownRef.current
    pointerDownRef.current = null
    setDragging(false)
    if (!down) return
    const hit = pointerAt(event)
    if (!hit) return
    // A click without drag: pick bracket in picker mode, else select a
    // segment, else seek.
    if (!down.moved) {
      if (pickMode) {
        onPick(hit.at)
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
      className={`relative touch-none select-none ${disabled ? 'cursor-not-allowed' : dragging ? 'cursor-grabbing' : 'cursor-crosshair'}`}
      style={{ height: HEIGHT }}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerLeave={() => {
        if (tooltipRef.current) tooltipRef.current.style.visibility = 'hidden'
      }}
      onDoubleClick={onDoubleClick}
      onWheel={onWheelZoom}
    >
      <canvas
        ref={canvasRef}
        style={{ width: '100%', height: HEIGHT }}
        className='pointer-events-none block rounded-md'
      />
      {/* Hover tooltip: the cursor's wall clock, or the hovered event. */}
      <div
        ref={tooltipRef}
        aria-hidden
        className='pointer-events-none absolute top-0 z-10 whitespace-nowrap rounded border bg-popover px-1.5 py-0.5 text-[10px] tabular-nums text-popover-foreground shadow-md'
        style={{ visibility: 'hidden', left: 0 }}
      />
      {/* Export bracket: translucent mask + drag handles. */}
      {bracketPx && (
        <div
          data-testid='playback-bracket'
          aria-label='导出区间'
          className='pointer-events-none absolute'
          style={{
            left: bracketPx.left,
            width: bracketPx.right - bracketPx.left,
            top: SEGMENTS_TOP,
            height: SEGMENTS_H,
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
      {/* Playhead: one absolutely-positioned cursor moved via transform. */}
      {playheadX !== null && (
        <div
          data-testid='playback-playhead'
          className='pointer-events-none absolute'
          style={{ left: 0, top: SEGMENTS_TOP, height: SEGMENTS_H, width: 2 }}
        >
          <div
            className='h-full bg-red-500 shadow-[0_0_8px_rgba(239,68,68,0.7)]'
            style={{ transform: `translateX(${playheadX - 1}px)` }}
          />
        </div>
      )}
    </div>
  )
}
