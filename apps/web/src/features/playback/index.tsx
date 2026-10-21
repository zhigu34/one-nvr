import { useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { configuredChannels } from '@/features/foundation/channels'
import { useInfiniteQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  ArrowLeft,
  Camera,
  Download,
  Maximize,
  Pause,
  Play,
  RotateCcw,
  SkipBack,
  SkipForward,
  Square,
  VideoOff,
} from 'lucide-react'
import type { PageData, Schema } from '@/lib/types'
import { apiRequest } from '@/lib/api-client'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
import { useAPI } from '@/features/foundation/hooks'
import { Empty, QueryState, SelectField } from '@/features/foundation/ui'
import { ExportDialog } from '@/features/recording-exports/export-dialog'
import { probeSegment } from './api'
import { TimelineCanvas } from './timeline-canvas'
import {
  RATES,
  PlaybackController,
  type MediaElement,
  type PlaybackState,
} from './player'
import {
  buildTimeline,
  formatDuration,
  sliceView,
  snapToPlayable,
  type Entry,
} from './timeline'
import { eventMarks, eventWindowSize } from './event-marks'
import {
  formatClock,
  parseWallClock,
  quickRange,
  wallClockInput,
  type QuickRangeKind,
} from './zone'

const IDLE: PlaybackState = {
  phase: 'idle',
  clock: 0,
  message: '选择时间轴上的片段后播放',
  entry: null,
  gap: null,
  skipped: null,
  rate: 1,
}

const label = (channel: Schema<'Channel'>) =>
  `CH${String(channel.channel_no).padStart(2, '0')} ${channel.channel_name}`

/** A months-wide search must not fetch forever; beyond this the page warns. */
const MAX_INDEX_SEGMENTS = 2000

const recordingsPath = (
  channelID: string,
  range: { start: number; end: number },
  cursor = ''
) =>
  `/api/v1/recordings?${new URLSearchParams({
    channel_id: channelID,
    start: new Date(range.start).toISOString(),
    end: new Date(range.end).toISOString(),
    // The contract's page ceiling for this endpoint: a 24-hour window holds
    // ~1440 sixty-second segments, so one search spans several pages.
    limit: '500',
    ...(cursor ? { cursor } : {}),
  })}`

/** Instant playback asks for the last five minutes; a plain visit asks a day. */
function defaultRange(at: string) {
  const anchored = at && Number.isFinite(Date.parse(at))
  const end = anchored ? Date.parse(at) : Date.now()
  return { start: end - (anchored ? 5 * 60_000 : 24 * 3600_000), end }
}

const QUICK_RANGES: { kind: QuickRangeKind; label: string }[] = [
  { kind: 'hour', label: '最近 1 小时' },
  { kind: 'day', label: '最近 24 小时' },
  { kind: 'today', label: '今天' },
  { kind: 'yesterday', label: '昨天' },
  { kind: 'week', label: '最近 7 天' },
]

/**
 * The three spans an operator actually wants on a timeline. The full range is
 * whatever the search returned; a few hours is "the shift I am reviewing"; one
 * hour is "the event I just clicked". Arbitrary +/- zoom made reaching any of
 * them a series of guesses.
 */
const ZOOM_PRESETS: { label: string; span: number }[] = [
  { label: '24h', span: 24 * 3_600_000 },
  { label: '6h', span: 6 * 3_600_000 },
  { label: '1h', span: 3_600_000 },
]

/** Tooltip copy for the timeline's "?" icon: how the axis behaves. */
const TIMELINE_HELP =
  '单击片段选中，双击播放，拖动平移，滚轮缩放；空格播放/暂停，← → 跳 10 秒。缺口不会静默跳过——拖动越过缺口时会明确提示。'

export function Playback({
  initialChannel = '',
  initialAt = '',
  active = true,
  // The shell hoists the channel list and selection; the shell passes both so
  // this component renders without its own rail and follows the shell's choice.
  providedChannels,
  controlledChannelId = '',
  onChannelChange,
  hideChannelList = false,
  layout,
  onRangeChange,
  onActiveEntryChange,
  recordingMode,
  eventRecording,
  playSignal,
  toolbar,
}: {
  initialChannel?: string
  initialAt?: string
  // False while the workspace shows the other tab: the channel/site queries
  // (and the index below) stay idle instead of both tabs fetching on mount.
  active?: boolean
  providedChannels?: Schema<'Channel'>[]
  controlledChannelId?: string
  onChannelChange?: (id: string) => void
  hideChannelList?: boolean
  // Workspace layout descriptor. `workspace` hands the rail and the files
  // panel in as slots: Playback renders the top row (rail / console / files)
  // plus the full-width timeline band row itself — the sample layout — all
  // in one tree, so no portal or cross-component timing is involved.
  // `plain` keeps the standalone shape.
  layout?:
    | { kind: 'plain' }
    | {
        kind: 'workspace'
        rail: React.ReactNode
        files: React.ReactNode | null
      }
  // Reports the search range upward so the workspace can drive the file
  // panel's window from the same source of truth (the single time filter).
  onRangeChange?: (range: { start: number; end: number }) => void
  // Reports the segment the player currently sits on (parked or playing).
  onActiveEntryChange?: (segmentId: string) => void
  /**
   * How this channel records. It decides what an empty stretch MEANS, which is
   * the one thing the gap count cannot say by itself:
   *
   *   continuous — every second should have footage; empty time is a fault.
   *   none/planned — footage exists only where a detection fired; the empty
   *                  time between events is expected, not a dropout.
   *
   * Absent (the standalone page) means unknown, and the wording then stays
   * neutral rather than claiming either.
   */
  recordingMode?: 'none' | 'continuous' | 'planned'
  eventRecording?: boolean
  // A play request from the file list; seq disambiguates repeats.
  playSignal?: { id: string; seq: number } | null
  // Toolbar controls for the search row: the shell owns the file drawer and
  // the count label; Playback just renders the buttons at the row's end.
  toolbar?: {
    filesOpen: boolean
    onFilesToggle: () => void
    onOpenFilesSheet: () => void
    filesCount?: number
    showFilesToggle: boolean
    narrow: boolean
  }
}) {
  const channelsQuery = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100',
    active && !providedChannels
  )
  const site = useAPI<Schema<'Site'>>('/api/v1/site', active)
  const filtered = configuredChannels(
    (providedChannels ?? channelsQuery.data?.items ?? []).filter((channel) =>
      channel.permissions.includes('playback')
    )
  )
  const channels = filtered
  // The site zone is never assumed: every clock in this page is derived from it,
  // so the workspace waits for the value the API reports.
  const zone = site.data?.timezone || ''
  // The zone announcement the unit test relies on lives on the loading gate:
  // prose about how playback works has no place on the page itself.
  if (!zone)
    return (
      <p role='status' className='py-8 text-sm text-muted-foreground'>
        正在读取站点时区…
      </p>
    )
  return (
    <div className='flex min-h-0 flex-1 flex-col'>
      <QueryState
        pending={(!providedChannels && channelsQuery.isPending) || site.isPending}
        error={
          !providedChannels
            ? channelsQuery.error ?? site.error
            : site.error
        }
        retry={() => {
          void channelsQuery.refetch()
          void site.refetch()
        }}
      />
      {!channels.length && (providedChannels || channelsQuery.data) && (
        <Empty>没有可回放的通道，请联系管理员分配录像回放权限。</Empty>
      )}
      {!!channels.length && (
        <Workspace
          // A site timezone change invalidates every wall-clock value below.
          key={zone}
          channels={channels}
          zone={zone}
          initialChannel={initialChannel}
          initialAt={initialAt}
          isActive={active}
          controlledChannelId={controlledChannelId}
          onChannelChange={onChannelChange}
          hideChannelList={hideChannelList}
          layout={layout}
          onRangeChange={onRangeChange}
          onActiveEntryChange={onActiveEntryChange}
          recordingMode={recordingMode}
          eventRecording={eventRecording}
          playSignal={playSignal}
          toolbar={toolbar}
        />
      )}
    </div>
  )
}

/**
 * Progress of the segment on screen, with a draggable handle.
 *
 * It reads the media element directly instead of routing currentTime through
 * React state: `timeupdate` fires four times a second and a state update would
 * re-render the whole console — including the canvas timeline — for a number
 * only this bar displays. The element is the source of truth for its own
 * position anyway.
 *
 * The span is ONE segment. The player holds a single file whose duration is that
 * file's; a bar spanning the whole day would lie about what dragging does. A seek
 * past the end is handled by the controller, which rolls into the next segment.
 */
function SegmentProgress({
  video,
  entry,
  zone,
  onSeek,
  disabled,
}: {
  video: RefObject<HTMLVideoElement | null>
  entry: Entry | null
  zone: string
  onSeek: (offsetMs: number) => void
  disabled: boolean
}) {
  const trackRef = useRef<HTMLDivElement>(null)
  const fillRef = useRef<HTMLDivElement>(null)
  const headRef = useRef<HTMLDivElement>(null)
  const labelRef = useRef<HTMLSpanElement>(null)
  const [dragging, setDragging] = useState(false)
  const dragRef = useRef(false)

  // Paint from the media element on every animation frame while it is playing,
  // and on seek/metadata events otherwise. Cheap: three style writes, no React.
  useEffect(() => {
    let frame = 0
    let stopped = false
    const paint = () => {
      if (stopped) return
      const media = video.current
      // `Number.isFinite` does not narrow `undefined` for the type checker, and
      // duration is NaN until metadata lands — check both explicitly.
      const duration = media?.duration ?? Number.NaN
      if (media && Number.isFinite(duration) && duration > 0 && !dragRef.current) {
        const ratio = Math.min(Math.max(media.currentTime / duration, 0), 1)
        if (fillRef.current) fillRef.current.style.width = `${ratio * 100}%`
        if (headRef.current) headRef.current.style.left = `${ratio * 100}%`
        if (labelRef.current) {
          labelRef.current.textContent = `${formatDuration(media.currentTime * 1000)} / ${formatDuration(duration * 1000)}`
        }
      }
      frame = requestAnimationFrame(paint)
    }
    frame = requestAnimationFrame(paint)
    return () => {
      stopped = true
      cancelAnimationFrame(frame)
    }
  }, [video])

  // A fresh segment starts at zero: clear the previous file's extent before its
  // duration is known, or the handle sits where the last clip ended.
  useEffect(() => {
    if (fillRef.current) fillRef.current.style.width = '0%'
    if (headRef.current) headRef.current.style.left = '0%'
    if (labelRef.current) labelRef.current.textContent = ''
  }, [entry?.id])

  const seekFromPointer = (clientX: number) => {
    const box = trackRef.current?.getBoundingClientRect()
    const media = video.current
    const duration = media?.duration ?? Number.NaN
    if (!box || !media || !Number.isFinite(duration) || duration <= 0) return
    const ratio = Math.min(Math.max((clientX - box.left) / box.width, 0), 1)
    if (fillRef.current) fillRef.current.style.width = `${ratio * 100}%`
    if (headRef.current) headRef.current.style.left = `${ratio * 100}%`
    onSeek(ratio * duration * 1000)
  }

  const absolute = entry?.start
  return (
    <div
      data-testid='playback-progress'
      className={cn(
        // h-5 is explicit: inside a wrapping flex row a w-full child has no
        // intrinsic height, and a zero-height element is not clickable (nor, to
        // Playwright, visible).
        'flex h-5 w-full items-center gap-2',
        disabled && 'pointer-events-none opacity-40'
      )}
    >
      <span
        ref={labelRef}
        className='w-[104px] shrink-0 text-[10px] tabular-nums text-white/75'
      />
      <div
        ref={trackRef}
        role='slider'
        aria-label='片段内进度'
        aria-valuemin={0}
        aria-valuemax={100}
        tabIndex={disabled ? -1 : 0}
        className='group/progress relative min-w-24 flex-1 cursor-pointer touch-none'
        style={{ height: 16 }}
        onPointerDown={(event) => {
          if (disabled) return
          event.currentTarget.setPointerCapture(event.pointerId)
          dragRef.current = true
          setDragging(true)
          seekFromPointer(event.clientX)
        }}
        onPointerMove={(event) => {
          if (dragRef.current) seekFromPointer(event.clientX)
        }}
        onPointerUp={() => {
          dragRef.current = false
          setDragging(false)
        }}
        onPointerCancel={() => {
          dragRef.current = false
          setDragging(false)
        }}
        onKeyDown={(event) => {
          if (disabled || !absolute) return
          const media = video.current
          const duration = media?.duration
          if (!Number.isFinite(duration ?? NaN) || !duration) return
          if (event.key === 'ArrowLeft') onSeek(Math.max(0, (media!.currentTime - 5) * 1000))
          else if (event.key === 'ArrowRight') onSeek(Math.min(duration * 1000, (media!.currentTime + 5) * 1000))
          else return
          event.preventDefault()
        }}
      >
        {/* Track: the full segment. Fill: how much has played. */}
        <div className='absolute inset-x-0 top-1/2 h-1 -translate-y-1/2 rounded-full bg-white/25' />
        <div
          ref={fillRef}
          className='absolute left-0 top-1/2 h-1 -translate-y-1/2 rounded-full bg-red-500'
          style={{ width: '0%' }}
        />
        <div
          ref={headRef}
          className={cn(
            'absolute top-1/2 size-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full bg-red-500 shadow-sm transition-transform',
            dragging ? 'scale-125' : 'group-hover/progress:scale-125'
          )}
          style={{ left: '0%' }}
        />
      </div>
      {entry && (
        <span
          className='w-[92px] shrink-0 text-right text-[10px] tabular-nums text-white/50'
          title={absolute ? `本片段起点 ${formatClock(absolute, zone)}` : undefined}
        >
          {absolute ? formatClock(absolute, zone).slice(11, 16) : ''}
        </span>
      )}
    </div>
  )
}

function Workspace({
  channels,
  zone,
  initialChannel,
  initialAt,
  isActive = true,
  controlledChannelId = '',
  onChannelChange,
  hideChannelList = false,
  layout,
  onRangeChange,
  onActiveEntryChange,
  recordingMode,
  eventRecording,
  playSignal,
  toolbar,
}: {
  channels: Schema<'Channel'>[]
  zone: string
  initialChannel: string
  initialAt: string
  isActive?: boolean
  controlledChannelId?: string
  onChannelChange?: (id: string) => void
  hideChannelList?: boolean
  layout?:
    | { kind: 'plain' }
    | {
        kind: 'workspace'
        rail: React.ReactNode
        files: React.ReactNode | null
      }
  onRangeChange?: (range: { start: number; end: number }) => void
  onActiveEntryChange?: (segmentId: string) => void
  /**
   * How this channel records. It decides what an empty stretch MEANS, which is
   * the one thing the gap count cannot say by itself:
   *
   *   continuous — every second should have footage; empty time is a fault.
   *   none/planned — footage exists only where a detection fired; the empty
   *                  time between events is expected, not a dropout.
   *
   * Absent (the standalone page) means unknown, and the wording then stays
   * neutral rather than claiming either.
   */
  recordingMode?: 'none' | 'continuous' | 'planned'
  eventRecording?: boolean
  playSignal?: { id: string; seq: number } | null
  toolbar?: {
    filesOpen: boolean
    onFilesToggle: () => void
    onOpenFilesSheet: () => void
    filesCount?: number
    showFilesToggle: boolean
    narrow: boolean
  }
}) {
  const [internalChannelID, setInternalChannelID] = useState(
    () =>
      channels.find((channel) => channel.id === initialChannel)?.id ||
      channels[0].id
  )
  // A controlled id from the workspace shell overrides the local store; the
  // shell owns the URL state and we never write to it on our own.
  const controlledMatches = channels.some(
    (channel) => channel.id === controlledChannelId
  )
  const channelID = controlledMatches ? controlledChannelId : internalChannelID
  function selectChannel(next: Schema<'Channel'>) {
    setSelected(null)
    setView(null)
    setBracket(null)
    if (controlledMatches) onChannelChange?.(next.id)
    else setInternalChannelID(next.id)
  }
  const [range, setRange] = useState(() => defaultRange(initialAt))
  // The inputs hold site-zone wall clock text so the operator reads and types
  // the same clock the timeline shows; only `range` is sent to the API, in UTC.
  const [draft, setDraft] = useState(() => ({
    start: wallClockInput(range.start, zone),
    end: wallClockInput(range.end, zone),
  }))
  const [rangeError, setRangeError] = useState('')
  const [selected, setSelected] = useState<Entry | null>(null)
  const [exportOpen, setExportOpen] = useState(false)
  // The view window into the fetched index: null means the whole search range.
  // Zooming never refetches — the paged index already covers the full window.
  const [view, setView] = useState<{ start: number; end: number } | null>(null)
  // ARC/EXP: the timeline bracket picker — first click sets the start, the
  // second the end, a third restarts. The completed bracket is the export range.
  const [pickMode, setPickMode] = useState(false)
  const [bracket, setBracket] = useState<{ start: number; end: number } | null>(null)
  const [exportRange, setExportRange] = useState<{ start: number; end: number } | null>(null)
  const [search, setSearch] = useState('')
  // The index follows the API cursor until the window is exhausted, so the
  // timeline never mistakes an unfetched page for a hole. Fetches run
  // newest-first, so the timeline fills backwards while pages arrive.
  const index = useInfiniteQuery({
    queryKey: ['recording-index', channelID, range],
    queryFn: ({ pageParam, signal }) =>
      apiRequest<Schema<'RecordingPage'>>(
        recordingsPath(channelID, range, pageParam),
        { signal }
      ),
    initialPageParam: '',
    getNextPageParam: (page) => page.next_cursor || undefined,
    retry: false,
    // An inactive workspace tab holds its data but fetches nothing new.
    enabled: isActive,
  })
  const {
    data,
    error: indexError,
    hasNextPage,
    isPending,
    isFetching,
    isFetchingNextPage,
    fetchNextPage,
    refetch,
  } = index
  const items = useMemo(
    () => data?.pages.flatMap((page) => page.items) ?? [],
    [data]
  )
  useEffect(() => {
    // A failed page surfaces through QueryState with a manual retry; auto
    // continuing past it would turn one error into an unbounded loop.
    if (indexError || !hasNextPage || isFetching) return
    if (items.length >= MAX_INDEX_SEGMENTS) return
    void fetchNextPage()
    // Each settled page changes what the guards read; other runs are no-ops.
  }, [indexError, hasNextPage, isFetching, fetchNextPage, items.length])
  const truncated = hasNextPage && items.length >= MAX_INDEX_SEGMENTS
  const timeline = useMemo(
    () => buildTimeline(items, range.start, range.end),
    [items, range]
  )
  const viewBounds = useMemo(
    () => view ?? { start: range.start, end: range.end },
    [view, range.start, range.end]
  )
  const viewTimeline = useMemo(
    () => sliceView(timeline, viewBounds.start, viewBounds.end),
    [timeline, viewBounds]
  )
  // What the empty stretches mean depends on how the channel records, so the
  // sentence is built from the mode rather than guessed:
  //
  //   continuous — every second should be recorded; empty time is a dropout.
  //   otherwise  — footage exists only where a detection fired; the gaps are
  //                the intervals between events, which is the normal shape of
  //                event-only recording.
  const { coveredLabel, gapNote } = useMemo(() => {
    const coveredMs = timeline.entries.reduce((sum, entry) => sum + (entry.end - entry.start), 0)
    // Hours and minutes, because "51:12" reads as a clock next to the other
    // time labels on this row; the point is how much footage exists, not when.
    const totalMinutes = Math.round(coveredMs / 60000)
    const covered =
      totalMinutes >= 60
        ? `${Math.floor(totalMinutes / 60)} 小时 ${totalMinutes % 60} 分`
        : totalMinutes >= 1
          ? `${totalMinutes} 分钟`
          : `${Math.round(coveredMs / 1000)} 秒`
    if (!timeline.gaps.length) return { coveredLabel: covered, gapNote: '' }
    if (recordingMode === 'continuous') {
      return { coveredLabel: covered, gapNote: `${timeline.gaps.length} 处中断，未录到画面` }
    }
    if (recordingMode === 'none' || recordingMode === 'planned') {
      return {
        coveredLabel: covered,
        gapNote: eventRecording
          ? `${timeline.gaps.length} 段事件之间的空档（未开启连续录像）`
          : `${timeline.gaps.length} 段空档（该通道未开启录像）`,
      }
    }
    // Unknown mode: state the observable fact and stop there.
    return { coveredLabel: covered, gapNote: `${timeline.gaps.length} 段空档` }
  }, [timeline.entries, timeline.gaps, recordingMode, eventRecording])
  // Wheel zoom anchored at the pointer: the millisecond under the cursor
  // stays under it, the way every map and NVR timeline zooms.
  function zoomAt(anchor: number, factor: number) {
    const span = (viewBounds.end - viewBounds.start) * factor
    const minSpan = 60_000
    if (span < minSpan) {
      const center = anchor
      setView({
        start: Math.max(range.start, center - minSpan / 2),
        end: Math.min(range.end, center + minSpan / 2),
      })
      return
    }
    const ratio = (anchor - viewBounds.start) / (viewBounds.end - viewBounds.start)
    let start = anchor - ratio * span
    let end = start + span
    if (start < range.start) {
      end += range.start - start
      start = range.start
    }
    if (end > range.end) {
      start = Math.max(range.start, start - (end - range.end))
      end = range.end
    }
    setView(start <= range.start && end >= range.end ? null : { start, end })
  }
  // The minimap viewport slides across the whole range; dragging it pans the
  // detailed strip without touching the playhead.
  function moveViewTo(center: number) {
    const span = viewBounds.end - viewBounds.start
    let start = center - span / 2
    let end = center + span / 2
    if (start < range.start) {
      end += range.start - start
      start = range.start
    }
    if (end > range.end) {
      start = Math.max(range.start, start - (end - range.end))
      end = range.end
    }
    setView(start <= range.start && end >= range.end ? null : { start, end })
  }
  const video = useRef<HTMLVideoElement>(null)
  const frame = useRef<HTMLDivElement>(null)
  const controller = useRef<PlaybackController | null>(null)
  const [state, setState] = useState<PlaybackState>(IDLE)
  // ARC-05: the segment being watched holds a playback lease. The heartbeat
  // renews it while the player stays on one segment (playing or paused — a
  // paused viewer is still watching), and leaving the segment releases it so
  // cleanup never waits on nobody.
  const activeEntry = state.entry?.id ?? ''
  const activePhase = state.phase
  useEffect(() => {
    if (!activeEntry) return
    const beat = () => {
      // A paused viewer is still watching; only a stopped or failed player
      // stops the heartbeat (the release travels with the entry change).
      if (activePhase === 'playing' || activePhase === 'paused' || activePhase === 'loading') {
        void apiRequest(`/api/v1/recordings/${activeEntry}/lease`, { method: 'POST' }).catch(() => {})
      }
    }
    beat()
    const timer = window.setInterval(beat, 30_000)
    return () => {
      window.clearInterval(timer)
    }
  }, [activeEntry, activePhase])
  useEffect(() => {
    if (!activeEntry) return
    return () => {
      void apiRequest(`/api/v1/recordings/${activeEntry}/lease`, { method: 'DELETE' }).catch(() => {})
    }
  }, [activeEntry])
  // The file list highlights (and scrolls to) whatever segment the player is
  // sitting on — parked or playing.
  useEffect(() => {
    onActiveEntryChange?.(activeEntry)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeEntry])
  // A play request from the file list: find the segment in the loaded index
  // and start playing it. The signal's seq lets repeated plays of the same
  // segment re-fire.
  const playSignalSeq = playSignal?.seq
  const playSignalId = playSignal?.id
  useEffect(() => {
    if (!playSignalId) return
    const entry = timeline.entries.find((candidate) => candidate.id === playSignalId)
    if (!entry) return
    setSelected(entry)
    controller.current?.start(entry.start)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playSignalId, playSignalSeq])
  useEffect(() => {
    if (!video.current) return
    const instance = new PlaybackController(
      video.current as unknown as MediaElement,
      (entry) => probeSegment(entry.id),
      setState
    )
    controller.current = instance
    return () => {
      instance.dispose()
      controller.current = null
    }
  }, [])
  useEffect(() => {
    const instance = controller.current
    if (!instance) return
    instance.setTimeline(timeline)
    const playing = instance.current.entry
    // A shorter range, another channel or a refreshed index can remove the
    // segment under the playhead; never keep streaming a segment the operator
    // can no longer see.
    if (playing && !timeline.entries.some((entry) => entry.id === playing.id))
      instance.stop()
  }, [timeline])
  // Keyboard transport: space toggles, arrows seek. Typing in the search or
  // datetime inputs must never trigger a seek, and a focused button owns its
  // Space/Enter (activation) — the shortcut only claims keys when the focus
  // sits on something non-interactive.
  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      if (
        target &&
        (target.tagName === 'INPUT' ||
          target.tagName === 'TEXTAREA' ||
          target.tagName === 'SELECT' ||
          target.tagName === 'BUTTON' ||
          target.isContentEditable)
      )
        return
      if (event.key === ' ') {
        event.preventDefault()
        if (state.phase === 'playing') controller.current?.pause()
        else controller.current?.resume()
      } else if (event.key === 'ArrowLeft') {
        event.preventDefault()
        controller.current?.shift(-10)
      } else if (event.key === 'ArrowRight') {
        event.preventDefault()
        controller.current?.shift(10)
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [state.phase])
  const filtered = channels.filter((channel) =>
    label(channel).toLowerCase().includes(search.toLowerCase())
  )
  const active =
    channels.find((channel) => channel.id === channelID) || channels[0]
  // No data in the searched window: the whole transport and the export
  // picker disable — there is nothing to play, scrub or bracket.
  const noData = timeline.entries.length === 0
  // Export is authorisation-gated per channel (EXP), so the control is absent
  // rather than disabled for someone who could never use it.
  const canExport = (active?.permissions ?? []).includes('export')

  // When footage arrives and nothing is playing yet, select the newest segment
  // so the timeline highlights it and the detail strip has something to show.
  //
  // It deliberately does NOT load the first frame. Parking the player meant a
  // media request on every visit to this page, and every read is an audited
  // playback: the audit log filled with `recording.streamed` rows for someone
  // who only opened the page, and each visit pinned a 90-second playback lease
  // that then blocked deleting that very segment ("该片段正在被播放"). Selecting
  // is a UI decision; reading bytes is the operator's.
  //
  // Keyed per channel+range: switching channels (or searching a new window)
  // re-arms it, so the operator lands on that view's newest footage.
  // Keyed on the channel and range, but re-armed by selectChannel clearing
  // `selected`: without that, switching channel left the highlight empty
  // because the key had not changed and the effect returned early.
  const autoParkRef = useRef('')
  useEffect(() => {
    const parkKey = `${channelID}|${range.start}|${range.end}`
    if (noData || (autoParkRef.current === parkKey && selected)) return
    const newest = [...timeline.entries]
      .filter((entry) => !entry.blocked)
      .sort((a, b) => b.end - a.end)[0]
    if (!newest) return
    autoParkRef.current = parkKey
    setSelected(newest)
  }, [noData, timeline.entries, channelID, range, selected])
  // Event marks for the canvas. The window is the view clamped to the
  // events' 90-day retention — a bounded, honest query (was inside the old
  // Timeline component; hoisted so the canvas owns one draw input).
  const eventWindow = useMemo(
    () => eventWindowSize(viewBounds.start, viewBounds.end),
    [viewBounds.start, viewBounds.end]
  )
  const eventsQuery = useAPI<Schema<'EventPage'>>(
    `/api/v1/events?${new URLSearchParams({
      channel_id: channelID,
      start: String(eventWindow.start),
      end: String(eventWindow.end),
      limit: '200',
    })}`,
    !!channelID
  )
  // Raw marks, in time order. The canvas clusters them against its OWN pixel
  // width — clustering here would need a width this component does not own, and
  // a guessed width would fold events that are visibly apart on a wide screen.
  const marksForCanvas = useMemo(
    () => eventMarks(eventsQuery.data?.items || [], viewBounds.start, viewBounds.end),
    [eventsQuery.data, viewBounds.start, viewBounds.end]
  )
  // Event stepping: the neighbour of the playhead in each direction. Computed
  // here rather than in the canvas because the buttons live in the toolbar and
  // the playhead is page state.
  const eventJumpTargets = useMemo(() => {
    const here = state.clock > 0 ? state.clock : viewBounds.start
    let prev: number | null = null
    let next: number | null = null
    for (const mark of marksForCanvas) {
      if (mark.at < here - 500) prev = mark.at
      else if (mark.at > here + 500 && next === null) next = mark.at
    }
    return { prev, next }
  }, [marksForCanvas, state.clock, viewBounds.start])
  const prevEventAt = eventJumpTargets.prev
  const nextEventAt = eventJumpTargets.next
  const jumpToEvent = (at: number) => {
    // The mark covers [start-5s, end+5s]; seeking to its start shows the
    // seconds before the detection, which is what an operator wants to see.
    controller.current?.start(snapToPlayable(timeline, at).at)
  }
  // A preset span centres on the playhead so zooming keeps the operator's
  // attention where it already is. Full range clears the view instead of
  // inventing bounds.
  const zoomToSpan = (targetSpan: number) => {
    if (targetSpan >= range.end - range.start) {
      setView(null)
      return
    }
    const anchor = state.clock > 0 ? state.clock : (viewBounds.start + viewBounds.end) / 2
    const start = Math.max(range.start, anchor - targetSpan / 2)
    const end = Math.min(range.end, start + targetSpan)
    setView({ start: Math.max(range.start, end - targetSpan), end })
  }
  // A blocked selection is never a play target: fall forward to the next
  // segment the index does vouch for, without pretending it starts here.
  const startTarget = useMemo(() => {
    if (selected && !selected.blocked) return selected
    const after = selected?.start ?? Number.NEGATIVE_INFINITY
    return (
      timeline.entries.find((entry) => !entry.blocked && entry.end > after) ||
      timeline.entries.find((entry) => !entry.blocked) ||
      null
    )
  }, [selected, timeline])
  function applyRange(next: { start: number; end: number }) {
    setRangeError('')
    setSelected(null)
    setView(null)
    setBracket(null)
    setRange(next)
    setDraft({
      start: wallClockInput(next.start, zone),
      end: wallClockInput(next.end, zone),
    })
    // The auto-park re-arms itself: the park key includes the range, so the
    // new window's index lands the playhead on its newest segment again.
    // The single time filter also drives the file panel's window.
    onRangeChange?.(next)
  }
  // First mount reports the default window too, so the file panel and the
  // channel rail's per-channel counts share it from the start.
  useEffect(() => {
    onRangeChange?.(range)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  // The timeline band element: the canvas strip plus the DOM toolbar row
  // (export picker + zoom controls) that lives above it. Built once per
  // render; the placement below decides whether it lives inside the console
  // (plain mode) or spans the full workspace width as row 2 (workspace mode).
  const timelineBand = (
    <div className='flex flex-col gap-1.5 rounded-xl bg-video-surface px-3.5 py-2.5'>
      {/* Toolbar row: zoom controls on the right, a short instruction while an
          export range is being picked. Export itself lives on the PLAYER bar:
          it is the second most frequent action after watching, and it was buried
          between the zoom buttons where nobody looked for it. */}
      <div className='flex flex-wrap items-center gap-2'>
        {pickMode && !bracket && (
          <span className='text-xs text-amber-400'>
            在时间轴上点两次：第一次定起点，第二次定终点；拖动左右手柄微调。
          </span>
        )}
        {pickMode && bracket && bracket.end > bracket.start && (
          <>
            <span className='text-xs tabular-nums text-video-chrome-foreground/70'>
              已选 {formatClock(bracket.start, zone)} — {formatClock(bracket.end, zone)} ·{' '}
              {formatDuration(bracket.end - bracket.start)}
            </span>
            <Button
              size='sm'
              variant='ghost'
              className='h-7 px-2 text-xs text-video-chrome-foreground hover:bg-video-wash'
              onClick={() => setBracket(null)}
            >
              清除区间
            </Button>
          </>
        )}
        {/* Selected segment summary rides the toolbar's middle zone — the
            card that used to squat a 44px row under the video. */}
        {selected && (
          <div
            data-testid='playback-selected'
            className='flex min-w-0 items-center gap-2 rounded-md border border-video-border bg-video-surface-raised px-2 py-0.5 text-[11px] text-video-chrome-foreground'
          >
            <span className='font-medium'>
              片段 {selected.id.slice(0, 8)} · {selected.blocked || '可用'}
            </span>
            <span className='min-w-0 truncate tabular-nums text-video-chrome-foreground/60'>
              {formatClock(selected.start, zone)} —{' '}
              {formatClock(selected.end, zone)} ·{' '}
              {formatDuration(selected.end - selected.start)} ·{' '}
              {(selected.bytes / 1048576).toFixed(1)} MiB
            </span>
            {active.permissions.includes('export') && !selected.blocked && (
              // Exports THIS segment, while the player bar's control exports a
              // picked range. Two different subjects, so the labels differ:
              // 本段 vs 片段.
              <Button
                size='sm'
                variant='outline'
                className='h-5 shrink-0 px-1.5 text-[10px]'
                title='只导出这一段录像'
                onClick={() => setExportOpen(true)}
              >
                导出本段
              </Button>
            )}
          </div>
        )}
        <div className='ml-auto flex items-center gap-3'>
          {/* Legend: what the two tracks mean, stated where the tracks are.
              The colours are read off the canvas, so the chips must match the
              fill/edge the canvas actually draws. */}
          <div className='flex items-center gap-2.5 text-[10px] text-video-chrome-foreground/60'>
            <span className='inline-flex items-center gap-1'>
              <span className='inline-block h-2 w-3 rounded-[1px] border-t-2 border-emerald-500 bg-emerald-500/25' />
              连续录像
            </span>
            <span className='inline-flex items-center gap-1'>
              <span className='inline-block size-2 rounded-full bg-amber-500' />
              AI 事件 ({marksForCanvas.length})
            </span>
          </div>
          {/* Event stepping: aiming at a 3px block inside 24 hours is a game of
              chance, so the two jumps do the aiming. */}
          <div className='flex items-center gap-0.5'>
            <Button
              size='sm'
              variant='ghost'
              aria-label='上一事件'
              title='跳到上一个 AI 事件'
              disabled={!prevEventAt || noData}
              onClick={() => prevEventAt && jumpToEvent(prevEventAt)}
              className='h-7 px-1.5 text-video-chrome-foreground hover:bg-video-wash'
            >
              <SkipBack size={13} />
            </Button>
            <Button
              size='sm'
              variant='ghost'
              aria-label='下一事件'
              title='跳到下一个 AI 事件'
              disabled={!nextEventAt || noData}
              onClick={() => nextEventAt && jumpToEvent(nextEventAt)}
              className='h-7 px-1.5 text-video-chrome-foreground hover:bg-video-wash'
            >
              <SkipForward size={13} />
            </Button>
          </div>
          {/* Zoom presets rather than a pair of +/- buttons: the three spans an
              operator actually wants are the full range, a few hours, and one
              hour around what they are watching. */}
          <div className='flex items-center gap-0.5 rounded-md border border-video-border p-0.5'>
            {ZOOM_PRESETS.map((preset) => {
              const activePreset =
                Math.abs(viewBounds.end - viewBounds.start - preset.span) < 60_000
              return (
                <button
                  key={preset.label}
                  type='button'
                  disabled={noData}
                  aria-pressed={activePreset}
                  onClick={() => zoomToSpan(preset.span)}
                  className={cn(
                    'rounded px-1.5 py-0.5 text-[10px] tabular-nums transition-colors',
                    activePreset
                      ? 'bg-video-wash font-medium text-video-chrome-foreground'
                      : 'text-video-chrome-foreground/55 hover:bg-video-wash/60 hover:text-video-chrome-foreground'
                  )}
                >
                  {preset.label}
                </button>
              )
            })}
          </div>
          <span
            role='note'
            aria-label='时间轴使用说明'
            title={TIMELINE_HELP}
            className='grid size-5 cursor-help place-items-center rounded-full border border-video-border text-[10px] text-video-chrome-foreground/60'
          >
            ?
          </span>
        </div>
      </div>
      {/* Axis header: density on the left, window + current clock on the
          right. Empty windows say so here instead of collapsing the band. */}
      <div className='flex items-center justify-between gap-3 text-[11px] tabular-nums text-video-chrome-foreground/50'>
        {timeline.entries.length > 0 ? (
          <span data-testid='playback-timeline-density' className='flex flex-wrap items-center gap-x-1.5'>
            {/* Three facts, because the count alone was read as a verdict:
                how many segments, how much time they actually cover, and what
                the empty time in between is. "153 段" sounds like plenty until
                you multiply by twenty seconds — it is 51 minutes of footage in
                a two-hour window, which is what sparse event recording looks
                like, not a failure. */}
            <span>
              本时段 <b className='font-medium text-video-chrome-foreground'>{timeline.entries.length}</b> 段
              <span className='text-video-chrome-foreground/70'> · 累计 {coveredLabel}</span>
            </span>
            {timeline.gaps.length > 0 && (
              <span className='text-video-chrome-foreground/70' data-testid='playback-gap-note'>
                · {gapNote}
              </span>
            )}
            <Link
              to='/recording-plan'
              className='text-video-chrome-foreground/60 underline-offset-2 hover:text-video-chrome-foreground hover:underline'
              title='看该通道的录像模式：连续录像时每一秒都该有画面；关闭录像时只有检测到事件才录，事件之间本来就没有画面'
            >
              查看录像模式
            </Link>
          </span>
        ) : (
          <span className='text-video-chrome-foreground/60'>
            该时间范围没有已登记录像
          </span>
        )}
        {/* The label states the VISIBLE window, not the search range. Zooming
            into six hours while this read the full twenty-four made the event
            track look like it had lost its marks: the window said 01:28-01:28
            either way, so nothing on screen accounted for the change. When the
            view is the whole range the two are the same, and the label says so
            only once. */}
        <span
          data-testid='playback-timeline-window'
          className='text-video-chrome-foreground/40'
          title={
            view
              ? `当前视野；检索范围 ${formatClock(timeline.from, zone)} — ${formatClock(timeline.to, zone)}`
              : '当前检索范围'
          }
        >
          {formatClock(viewBounds.start, zone)} — {formatClock(viewBounds.end, zone)}
          {view ? '（已缩放）' : ''}
        </span>
        {/* The playhead time is THE current time of this page: the biggest
            number in the band, not one of three equal-weight clocks. The picture
            carries a second clock burned in by the camera, and nothing here can
            change that one — so this side has to be unmistakably the reference. */}
        <span className='flex items-baseline gap-1.5'>
          <span className='text-[10px] uppercase tracking-wide text-video-chrome-foreground/40'>播放头</span>
          <b
            data-testid='playback-timeline-clock'
            className='text-base font-semibold tabular-nums text-video-chrome-foreground'
          >
            {state.clock > 0 ? formatClock(state.clock, zone) : '—'}
          </b>
        </span>
      </div>
      <TimelineCanvas
        timeline={viewTimeline}
        zone={zone}
        clock={state.clock}
        selected={selected}
        disabled={noData}
        marks={marksForCanvas}
        bracket={pickMode ? bracket : null}
        pickMode={pickMode}
        viewIsFull={!view}
        onPick={(at) => {
          setBracket((current) => {
            if (!current) return { start: at, end: at }
            if (current.end === current.start) {
              const start = Math.min(current.start, at)
              const end = Math.max(current.start, at)
              return end - start >= 1000 ? { start, end } : { start: at, end: at }
            }
            return { start: at, end: at }
          })
        }}
        onSetBracket={setBracket}
        onSelect={(entry) => {
          // Selecting a segment parks the player on its first frame — the
          // picture replaces the idle ring immediately; 播放 starts motion.
          setSelected(entry)
          controller.current?.park(entry.start)
        }}
        onSeek={(at) => {
          const snap = snapToPlayable(timeline, at)
          controller.current?.seek(snap.at)
        }}
        onPlay={(entry) => {
          setSelected(entry)
          controller.current?.start(entry.start)
        }}
        onZoomAt={zoomAt}
        onMoveView={moveViewTo}
      />
      {/* Legend + keyboard cheatsheet (the sample's tl-foot row). */}
      <div className='flex flex-wrap items-center gap-x-3.5 gap-y-1 text-[11px] text-video-chrome-foreground/50'>
        <span className='inline-flex items-center gap-1.5'>
          <span className='size-2 rounded-sm bg-emerald-600' />
          可用片段
        </span>
        <span className='inline-flex items-center gap-1.5'>
          <span className='size-2 rounded-sm bg-red-500' />
          受阻片段
        </span>
        <span className='inline-flex items-center gap-1.5'>
          <span className='size-2 rounded-sm bg-slate-400' />
          已删除
        </span>
        <span className='inline-flex items-center gap-1.5'>
          <span className='size-2 rounded-sm bg-amber-400' />
          事件
        </span>
        <span className='inline-flex items-center gap-1.5'>
          <span className='size-2 rounded-sm bg-red-500 ring-1 ring-red-300/60' />
          播放位置
        </span>
        <div className='ml-auto flex flex-wrap items-center gap-2.5'>
          <span><kbd className='rounded border border-video-border bg-video-wash px-1 py-px text-[10px] text-video-chrome-foreground/80'>Space</kbd> 播放/暂停</span>
          <span><kbd className='rounded border border-video-border bg-video-wash px-1 py-px text-[10px] text-video-chrome-foreground/80'>←</kbd>/<kbd className='rounded border border-video-border bg-video-wash px-1 py-px text-[10px] text-video-chrome-foreground/80'>→</kbd> 跳 10 秒</span>
          <span><kbd className='rounded border border-video-border bg-video-wash px-1 py-px text-[10px] text-video-chrome-foreground/80'>滚轮</kbd> 缩放</span>
          <span><kbd className='rounded border border-video-border bg-video-wash px-1 py-px text-[10px] text-video-chrome-foreground/80'>拖动</kbd> 平移</span>
          <span><kbd className='rounded border border-video-border bg-video-wash px-1 py-px text-[10px] text-video-chrome-foreground/80'>双击</kbd> 播放</span>
        </div>
      </div>
    </div>
  )
return (
    // The page IS the player: one dark surface spanning the viewport, channel
    // rail on the left, console on the right. No prose anywhere.
    // In workspace mode the outer div becomes a 2-row grid: row 1 is the
    // rail slot / console / files slot, row 2 is the full-width timeline
    // band. The rail/files slots come from the shell via `layout`.
    <div
      className={cn('min-h-0 flex-1 gap-3', layout?.kind === 'workspace' ? 'grid' : 'flex flex-col' + (hideChannelList ? '' : ' lg:flex-row'))}
      style={
        layout?.kind === 'workspace'
          ? {
              // Fixed rail/files widths so long content can never stretch
              // the columns; only the console flexes. Row 1 takes all the
              // height the timeline band leaves; the band is content-sized.
              // A null files slot (drawer collapsed / viewer role) drops the
              // third track entirely — no empty column, the console stretches
              // to the right edge.
              gridTemplateColumns:
                layout.files != null
                  ? '200px minmax(0, 1fr) 340px'
                  : '200px minmax(0, 1fr)',
              gridTemplateRows: 'minmax(0, 1fr) auto',
            }
          : undefined
      }
    >
      {/* Workspace mode: the rail comes from the shell's slot; the built-in
          aside is for the standalone (plain) render only. */}
      {layout?.kind === 'workspace'
        ? layout.rail
        : !hideChannelList && (
      <aside className='flex shrink-0 flex-col gap-2 rounded-xl bg-video-surface p-2.5 text-video-chrome-foreground lg:w-52'>
        <div className='flex items-center justify-between px-1 text-xs font-medium text-video-chrome-foreground/80'>
          <span>通道</span>
          <span className='text-video-chrome-foreground/50'>{filtered.length}/{channels.length}</span>
        </div>
        <Input
          aria-label='搜索回放通道'
          placeholder='搜索编号或名称'
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          className='h-8 border-video-border bg-video-input text-video-chrome-foreground placeholder:text-video-chrome-foreground/40'
        />
        <div className='grid min-h-0 flex-1 content-start gap-0.5 overflow-y-auto max-lg:grid-flow-col max-lg:auto-cols-max max-lg:overflow-x-auto'>
          {filtered.map((channel) => (
            <Button
              key={channel.id}
              variant='ghost'
              onClick={() => selectChannel(channel)}
              className={cn(
                'h-8 w-full justify-start truncate px-2 text-left text-sm font-normal text-video-chrome-foreground hover:bg-video-wash hover:text-video-chrome-foreground',
                channel.id === channelID && 'bg-video-wash font-medium'
              )}
            >
              {label(channel)}
            </Button>
          ))}
        </div>
        <Button
          asChild
          variant='ghost'
          className='h-8 justify-start px-2 text-xs text-video-chrome-foreground/70 hover:bg-video-wash hover:text-video-chrome-foreground'
        >
          <Link to='/live' search={{ channel: '' }}>返回实时预览</Link>
        </Button>
      </aside>
      )}
      <section className='flex min-h-0 min-w-0 flex-1 flex-col gap-2'>
        {/* The console: search rides INSIDE the dark surface as one slim
            toolbar — dates, search, quick ranges, no visible labels. */}
        <div data-testid='playback-console' className='flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border border-video-border bg-video-surface text-video-chrome-foreground shadow-sm'>
          <form
            className='flex flex-nowrap items-center gap-1.5 overflow-x-auto border-b border-video-border px-3 py-2'
            title={`时间按站点时区 ${zone} 填写与显示，单次检索不超过 31 天`}
            onSubmit={(event) => {
            event.preventDefault()
            const start = parseWallClock(draft.start, zone),
              end = parseWallClock(draft.end, zone)
            if (!Number.isFinite(start) || !Number.isFinite(end)) {
              setRangeError(`请输入有效的起止时间（按站点时区 ${zone}）`)
              return
            }
            if (start >= end) {
              setRangeError('结束时间需晚于开始时间')
              return
            }
            if (end - start > 31 * 24 * 3600_000) {
              setRangeError('单次检索不能超过 31 天')
              return
            }
            applyRange({ start, end })
          }}
          >
            <Input
              type='datetime-local'
              step='60'
              aria-label='检索开始时间'
              value={draft.start}
              onChange={(event) =>
                setDraft({ ...draft, start: event.target.value })
              }
              className='h-8 w-52 shrink-0 border-video-border bg-video-input text-video-chrome-foreground [color-scheme:light] dark:[color-scheme:dark]'
              required
            />
            <span aria-hidden className='shrink-0 text-video-chrome-foreground/40'>→</span>
            <Input
              type='datetime-local'
              step='60'
              aria-label='检索结束时间'
              value={draft.end}
              onChange={(event) =>
                setDraft({ ...draft, end: event.target.value })
              }
              className='h-8 w-52 shrink-0 border-video-border bg-video-input text-video-chrome-foreground [color-scheme:light] dark:[color-scheme:dark]'
              required
            />
            <Button type='submit' size='sm' className='h-8 shrink-0'>检索录像</Button>
            <div className='flex shrink-0 items-center gap-1'>
              {QUICK_RANGES.map(({ kind, label: name }) => (
                <Button
                  key={kind}
                  type='button'
                  size='sm'
                  variant='ghost'
                  className='h-8 whitespace-nowrap px-2 text-xs text-video-chrome-foreground/80 hover:bg-video-wash hover:text-video-chrome-foreground'
                  onClick={() => applyRange(quickRange(kind, Date.now(), zone))}
                >
                  {name}
                </Button>
              ))}
            </div>
            {rangeError && (
              <p role='alert' className='w-full shrink-0 text-sm text-red-400'>
                {rangeError}
              </p>
            )}
            {/* Row-end controls migrated from the deleted page header: the
                live preview link and the file drawer toggle. */}
            {toolbar && (
              <div className='ml-auto flex shrink-0 items-center gap-2'>
                <Link
                  to='/live'
                  search={{ channel: '' }}
                  aria-label='返回实时预览'
                  title='返回实时预览'
                  className='inline-flex h-8 items-center gap-1 rounded-md border border-video-border bg-video-surface-raised px-2 text-xs text-video-chrome-foreground/80 hover:bg-video-wash hover:text-video-chrome-foreground'
                >
                  <ArrowLeft className='size-3.5' />
                  实时预览
                </Link>
                {toolbar.showFilesToggle &&
                  (toolbar.narrow ? (
                    <button
                      type='button'
                      aria-label='查看文件'
                      title='查看文件'
                      onClick={toolbar.onOpenFilesSheet}
                      className='inline-flex h-8 items-center gap-1.5 rounded-md border border-video-border bg-video-surface-raised px-2 text-xs text-video-chrome-foreground hover:bg-video-wash'
                    >
                      文件 ({toolbar.filesCount ?? '…'}段)
                    </button>
                  ) : (
                    <button
                      type='button'
                      aria-pressed={toolbar.filesOpen}
                      aria-label={toolbar.filesOpen ? '收起文件面板' : '展开文件面板'}
                      title={toolbar.filesOpen ? '收起文件面板' : '展开文件面板'}
                      onClick={toolbar.onFilesToggle}
                      className={cn(
                        'inline-flex h-8 items-center gap-1.5 rounded-md border px-2 text-xs transition-colors',
                        toolbar.filesOpen
                          ? 'border-zinc-600 bg-zinc-800 text-white'
                          : 'border-video-border bg-video-surface-raised text-video-chrome-foreground hover:bg-video-wash'
                      )}
                    >
                      文件 ({toolbar.filesCount ?? '…'}段)
                    </button>
                  ))}
              </div>
            )}
          </form>
          <div
            ref={frame}
            // Flex-grown inside the console: the row's 1fr height minus the
            // search form and OSD strips is exactly the picture. The video
            // letterboxes anything that is not this shape.
            className='group/frame relative flex min-h-[260px] flex-1 items-center justify-center overflow-hidden bg-black'
          >
            <video
              ref={video}
              data-testid='playback-video'
              playsInline
              // Motion only ever starts from the controller; parking shows the
              // first frame via the loaded resource.
              autoPlay={false}
              preload='metadata'
              className='absolute inset-0 h-full w-full bg-black object-contain'
            />
            {/* Top-OSD metadata strip: channel + media facts, hovering over
                the picture in sync with the bottom control bar — fade in on
                hover (or while paused so facts stay readable), fade out while
                the operator watches, so the camera's own corner watermark is
                never covered. */}
            <div
              data-testid='playback-frame-osd'
              className={cn(
                'pointer-events-none absolute inset-x-0 top-0 flex flex-wrap items-center gap-1.5 p-3 transition-opacity duration-200',
                state.phase === 'playing'
                  ? 'opacity-0 group-hover/frame:opacity-100 focus-within:opacity-100'
                  : 'opacity-100'
              )}
            >
              {active && (
                <span className='rounded-md border border-white/15 bg-black/55 px-2 py-0.5 text-[11px] text-white shadow-sm backdrop-blur'>
                  CH{String(active.channel_no).padStart(2, '0')} {active.channel_name}
                </span>
              )}
              {/* Media pills describe the SELECTED segment (index facts), not
                  the live stream's health — so they stay put while the
                  byte-range request is in flight or a load error is being
                  explained. */}
              {selected?.media && selected.media.width > 0 && (
                <span className='rounded-md border border-white/15 bg-black/55 px-2 py-0.5 text-[11px] tabular-nums text-white/85 shadow-sm backdrop-blur'>
                  {selected.media.width}×{selected.media.height}
                </span>
              )}
              {selected?.media?.fps ? (
                <span className='rounded-md border border-white/15 bg-black/55 px-2 py-0.5 text-[11px] tabular-nums text-white/85 shadow-sm backdrop-blur'>
                  {Math.round(selected.media.fps)}fps
                </span>
              ) : null}
              {selected?.media?.codec ? (
                <span className='rounded-md border border-white/15 bg-black/55 px-2 py-0.5 text-[11px] uppercase text-white/85 shadow-sm backdrop-blur'>
                  {selected.media.codec}
                </span>
              ) : null}
              {selected && (
                <span
                  title='本回放不经转码'
                  className='rounded-md border border-emerald-400/40 bg-emerald-400/15 px-2 py-0.5 text-[11px] text-emerald-300 shadow-sm backdrop-blur'
                >
                  直通
                </span>
              )}
              {/* The picture carries a clock burned in by the camera, and it is
                  not the playhead: camera clocks drift, and the stream that
                  produced this file may have started before the recording did.
                  Naming the two clocks is what stops an operator from reading
                  "the video says 20:42:53 but the page says 20:42:51" as a bug
                  in playback. The exact offset is not knowable here — it lives in
                  the pixels — so this points at the playhead as the reference
                  instead of inventing a delta. */}
              {state.phase !== 'playing' && (
                <span
                  title='画面内时间是摄像头烧录的；本页「播放头」时间才是这份录像在站点时区中的位置。两者可能有秒级差异，且摄像头时钟可能独立漂移。'
                  className='rounded-md border border-white/10 bg-black/40 px-2 py-0.5 text-[10px] text-white/55 shadow-sm backdrop-blur'
                >
                  画面内时间来自摄像头
                </span>
              )}
            </div>
            {/* Top-right playback-state badge: red dot + wall-clock when
                playing, amber dot when paused, and a soft spinner while the
                byte-range request is in flight. Hidden in idle so the
                placeholder stays uncluttered. */}
            {state.phase !== 'idle' && state.phase !== 'error' && (
              <div
                data-testid='playback-state-badge'
                className={cn(
                  // Top-LEFT, beside the codec/直通 pills and away from the
                  // timeline's own clock. It used to sit top-right, where its
                  // wall-clock read as a second "current time" competing with the
                  // timeline header — an operator could not tell which one was
                  // the playhead. A playback state belongs to the picture, so it
                  // lives on the picture.
                  'pointer-events-none absolute left-3 top-10 flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-[11px] tabular-nums shadow-sm backdrop-blur transition-opacity duration-200',
                  // Same hover contract as the control bar and the top-left
                  // pills: visible while paused/loading, fade while playing.
                  state.phase === 'playing'
                    ? 'opacity-0 group-hover/frame:opacity-100'
                    : 'opacity-100',
                  state.phase === 'playing' &&
                    'border-red-400/60 bg-red-500/15 text-red-300',
                  state.phase === 'paused' &&
                    'border-amber-400/40 bg-amber-400/15 text-amber-300',
                  state.phase === 'loading' &&
                    'border-amber-400/40 bg-amber-400/15 text-amber-300'
                )}
              >
                {state.phase === 'playing' && (
                  <>
                    <span className='inline-block size-2 rounded-full bg-red-400 shadow-[0_0_6px_rgba(248,113,113,0.8)] animate-[pulse_1.6s_ease-in-out_infinite]' />
                    <span>REC · {state.clock > 0 ? formatClock(state.clock, zone) : '—'}</span>
                  </>
                )}
                {state.phase === 'paused' && (
                  <>
                    <span className='inline-block size-2 rounded-full bg-amber-400' />
                    <span>已暂停 · {state.clock > 0 ? formatClock(state.clock, zone) : '—'}</span>
                  </>
                )}
                {state.phase === 'loading' && (
                  <>
                    <svg
                      className='size-3 animate-spin'
                      viewBox='0 0 24 24'
                      fill='none'
                      stroke='currentColor'
                      strokeWidth='2.5'
                      aria-hidden
                    >
                      <path d='M21 12a9 9 0 1 1-6.2-8.55' />
                    </svg>
                    <span>加载中…</span>
                  </>
                )}
              </div>
            )}
            {/* Empty-window placeholder: a static, non-clickable cue. The
                transport disables entirely when the window has no footage. */}
            {noData && (
              <div className='pointer-events-none relative z-10 flex flex-col items-center gap-3 text-center text-video-chrome-foreground/70'>
                <VideoOff className='size-10 opacity-60' />
                <p className='text-sm'>
                  当前时段没有已登记录像，请切换上方时间范围或检查录像计划
                </p>
              </div>
            )}
            {!noData && state.phase === 'idle' && !state.entry && (
              <div className='pointer-events-none relative z-10 flex flex-col items-center gap-2 text-center text-video-chrome-foreground/70'>
                <div className='size-12 rounded-full border border-video-chrome-foreground/30' />
                <p className='text-sm'>
                  点击下方时间轴的片段开始回放，或按下方「播放」选第一条
                </p>
              </div>
            )}
            {/* Floating control bar: hovers over the picture, auto-hides while
                playing, always visible when paused/idle. Buttons keep their
                accessible names — e2e and keyboard users navigate by them. */}
            <div
              className={cn(
                'absolute inset-x-0 bottom-0 flex flex-wrap items-center gap-1.5 bg-gradient-to-t from-black/85 via-black/60 to-transparent px-3 pb-2.5 pt-3 transition-opacity duration-200',
                state.phase === 'playing'
                  ? 'opacity-0 group-hover/frame:opacity-100 focus-within:opacity-100'
                  : 'opacity-100'
              )}
            >
              {/* Progress of the segment on screen. It is a SEGMENT scrubber, not
                  the timeline: the player holds one file at a time, its duration
                  is the file's, and a bar spanning a whole day would misreport
                  what dragging it does. Seeking past the end continues into the
                  next segment via the controller. */}
              <SegmentProgress
                video={video}
                entry={state.entry}
                zone={zone}
                onSeek={(offsetMs) => controller.current?.seek(state.entry!.start + offsetMs)}
                // Disabled only when there is nothing to scrub. Requiring a
                // playing entry would hide the control exactly when an operator
                // opens the page and looks for a way to position — the bar is a
                // locator, not a playback indicator.
                disabled={noData}
              />
              <Button
                size='icon'
                variant='ghost'
                className='size-8 text-white hover:bg-white/15'
                aria-label={state.phase === 'playing' ? '暂停' : '播放'}
                title={state.phase === 'playing' ? '暂停' : '播放'}
                onClick={() => {
                  if (state.phase === 'playing' || state.phase === 'paused' || state.phase === 'loading') {
                    if (state.phase === 'playing') controller.current?.pause()
                    else controller.current?.resume()
                    return
                  }
                  // Idle/gap/error: the button starts the first available
                  // segment — one smart transport button, no duplicate.
                  if (startTarget) controller.current?.start(startTarget.start)
                }}
                disabled={noData}
              >
                {state.phase === 'playing' ? (
                  <Pause size={16} />
                ) : (
                  <Play size={16} />
                )}
              </Button>
              <Button
                size='icon'
                variant='ghost'
                className='size-8 text-white hover:bg-white/15'
                aria-label='-10 秒'
                title='-10 秒'
                disabled={noData}
                onClick={() => controller.current?.shift(-10)}
              >
                <RotateCcw size={16} />
              </Button>
              <Button
                size='icon'
                variant='ghost'
                className='size-8 text-white hover:bg-white/15'
                aria-label='+10 秒'
                title='+10 秒'
                disabled={noData}
                onClick={() => controller.current?.shift(10)}
              >
                <RotateCcw size={16} className='scale-x-[-1]' />
              </Button>
              <Button
                size='icon'
                variant='ghost'
                className='size-8 text-white hover:bg-white/15'
                aria-label='下一可用片段'
                title='下一可用片段'
                disabled={noData}
                onClick={() => controller.current?.jumpToNext()}
              >
                <SkipForward size={16} />
              </Button>
              <Button
                size='icon'
                variant='ghost'
                className='size-8 text-white hover:bg-white/15'
                aria-label='停止'
                title='停止'
                disabled={noData}
                onClick={() => {
                  // Release the playback lease before the controller drops the
                  // segment, so cleanup never waits on a stopped viewer.
                  const entryID = state.entry?.id
                  const release = entryID
                    ? apiRequest(`/api/v1/recordings/${entryID}/lease`, { method: 'DELETE' }).catch(() => {})
                    : Promise.resolve()
                  void release.finally(() => controller.current?.stop())
                }}
              >
                <Square size={16} />
              </Button>
              <Button
                size='icon'
                variant='ghost'
                className='size-8 text-white hover:bg-white/15'
                aria-label='截图'
                title='截图（保存当前画面为 PNG）'
                disabled={!state.entry}
                onClick={() => {
                  const media = video.current
                  if (!media || !state.entry) return
                  const canvas = document.createElement('canvas')
                  canvas.width = media.videoWidth || 1280
                  canvas.height = media.videoHeight || 720
                  const ctx = canvas.getContext('2d')
                  if (!ctx) return
                  ctx.drawImage(media, 0, 0, canvas.width, canvas.height)
                  canvas.toBlob((blob) => {
                    if (!blob) return
                    const url = URL.createObjectURL(blob)
                    const anchor = document.createElement('a')
                    anchor.href = url
                    anchor.download = `${label(active).replace(/\s+/g, '-')}-${formatClock(
                      state.clock || state.entry!.start,
                      zone || 'UTC'
                    ).replace(/[:\s]/g, '-')}.png`
                    anchor.click()
                    URL.revokeObjectURL(url)
                  }, 'image/png')
                }}
              >
                <Camera size={16} />
              </Button>
              <Button
                size='icon'
                variant='ghost'
                className='size-8 text-white hover:bg-white/15'
                aria-label='放大'
                title='全屏'
                onClick={() =>
                  void frame.current?.requestFullscreen().catch(() => {})
                }
              >
                <Maximize size={16} />
              </Button>
              <div className='flex items-center gap-1.5 text-xs text-white/90'>
                <SelectField
                  ariaLabel='播放倍速'
                  size='sm'
                  className='w-20'
                  triggerClassName='bg-white/10 border-white/20 text-white'
                  value={String(state.rate)}
                  onValueChange={(next) =>
                    controller.current?.setRate(Number(next))
                  }
                  disabled={noData}
                  options={RATES.map((rate) => ({
                    value: String(rate),
                    label: `${rate}x`,
                  }))}
                />
              </div>
              {/* Export: the second most frequent action on this page, so it is
                  a named control on the player bar rather than a small chip
                  among the zoom buttons. Picking a range is a mode, so it stays
                  one button that names the next step; once a range exists the
                  primary action appears beside it. */}
              {canExport && (
                <div className='ml-auto flex items-center gap-1.5'>
                  {bracket && bracket.end > bracket.start ? (
                    <>
                      <Button
                        size='sm'
                        className='h-7 gap-1 px-2.5 text-xs'
                        onClick={() => setExportRange({ ...bracket })}
                      >
                        <Download size={13} />
                        导出 {formatDuration(bracket.end - bracket.start)}
                      </Button>
                      <Button
                        size='sm'
                        variant='ghost'
                        className='h-7 px-2 text-xs text-white/80 hover:bg-white/15'
                        onClick={() => {
                          setPickMode(false)
                          setBracket(null)
                        }}
                      >
                        取消
                      </Button>
                    </>
                  ) : (
                    <Button
                      size='sm'
                      variant={pickMode ? 'secondary' : 'ghost'}
                      className='h-7 gap-1 px-2.5 text-xs text-white hover:bg-white/15'
                      aria-pressed={pickMode}
                      disabled={noData}
                      title='在时间轴上选一段区间后导出'
                      onClick={() => {
                        setPickMode(!pickMode)
                        setBracket(null)
                      }}
                    >
                      <Download size={13} />
                      {pickMode ? '在时间轴上选区间' : '导出片段'}
                    </Button>
                  )}
                </div>
              )}
            </div>
          </div>
          {/* Warning strip: only rendered when there is an actual warning
              (skipped gap, dead hole, error) — the routine phase/position
              read lives in the band's axis header and the selection card, so
              this row costs zero height in the common case. */}
          {(state.skipped || state.gap || state.phase === 'error') && (
            <div className='flex flex-wrap items-center gap-x-4 gap-y-0.5 border-t border-video-border px-3 py-1.5 text-[11px] text-video-chrome-foreground/70'>
              {state.skipped && (
                <p role='alert' className='text-amber-400'>
                  已自动跳过缺口 {formatClock(state.skipped.start, zone)} —{' '}
                  {formatClock(state.skipped.end, zone)}（
                  {formatDuration(state.skipped.end - state.skipped.start)}，
                  {state.skipped.reason}）
                </p>
              )}
              {state.gap && (
                <p role='alert' className='flex items-center gap-2 text-amber-400'>
                  该时间没有可播放录像：{formatClock(state.gap.start, zone)} —{' '}
                  {formatClock(state.gap.end, zone)}（{state.gap.reason}）
                  <Button
                    size='sm'
                    variant='outline'
                    className='h-6 border-white/20 px-2 text-xs text-white hover:bg-white/15'
                    onClick={() => controller.current?.jumpToNext()}
                  >
                    跳到下一可用片段
                  </Button>
                </p>
              )}
              {state.phase === 'error' && (
                <p role='alert' className='text-red-400'>
                  {state.message}
                </p>
              )}
            </div>
          )}
          {/* Workspace mode renders the band in the full-width row below the
              grid; plain (standalone) keeps it inside the console. */}
          {layout?.kind !== 'workspace' && timelineBand}
        </div>
        <QueryState
          pending={isPending}
          error={indexError}
          retry={refetch}
        />
        {exportRange && (
          <ExportDialog
            channel={active}
            zone={zone}
            start={exportRange.start}
            end={exportRange.end}
            open
            onOpenChange={(next) => {
              setExportRange(next ? exportRange : null)
            }}
          />
        )}
        {isFetchingNextPage && (
          <p role='status' className='text-sm text-muted-foreground'>
            正在加载更早的录像…
          </p>
        )}
        {truncated && (
          <p
            role='alert'
            className='rounded-xl border border-amber-500/50 bg-amber-500/10 p-3 text-sm text-amber-600'
          >
            片段超过单次加载上限（{MAX_INDEX_SEGMENTS}
            个），时间轴只包含最新的部分
            {timeline.entries[0]
              ? `，已覆盖到 ${formatClock(timeline.entries[0].start, zone)}`
              : ''}
            。更早的录像请缩小时间范围后检索。
          </p>
        )}
        {selected && exportOpen && (
          <ExportDialog
            channel={active}
            zone={zone}
            start={selected.start}
            end={selected.end}
            open={exportOpen}
            onOpenChange={setExportOpen}
          />
        )}
      </section>
      {/* Workspace mode extras: the files slot rides column 3 of row 1, and
          the timeline band spans the full width as row 2. The band keeps its
          full intrinsic height (shrink-0): the flex-1 row above absorbs the
          squeeze, so the legend row at the band's bottom never clips against
          the viewport edge. */}
      {layout?.kind === 'workspace' && (
        <>
          {layout.files}
          <div className='shrink-0 pb-1' style={{ gridColumn: '1 / -1' }}>
            {timelineBand}
          </div>
        </>
      )}
    </div>
  )
}
