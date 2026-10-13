import { useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type WheelEvent as ReactWheelEvent } from 'react'
import { useInfiniteQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  ChevronLeft,
  ChevronRight,
  Maximize,
  Maximize2,
  Pause,
  Play,
  RotateCcw,
  SkipForward,
  Square,
  ZoomIn,
  ZoomOut,
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
import {
  RATES,
  PlaybackController,
  type MediaElement,
  type PlaybackState,
} from './player'
import {
  buildTimeline,
  coverageDensity,
  formatDuration,
  buildTicks,
  sliceView,
  snapToPlayable,
  zoomView,
  type Entry,
  type Timeline,
} from './timeline'
import { clusterMarks, eventMarks, eventWindowSize, markLabel } from './event-marks'
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
]

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
}) {
  const channelsQuery = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100',
    active && !providedChannels
  )
  const site = useAPI<Schema<'Site'>>('/api/v1/site', active)
  const filtered = (providedChannels ?? channelsQuery.data?.items ?? []).filter(
    (channel) => channel.permissions.includes('playback')
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
        />
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
  // Minimap coverage over the whole search range, merged and bucketed coarsely
  // (the rail is ~120 buckets regardless of zoom).
  const fullDensity = useMemo(
    () => coverageDensity(timeline, 120),
    [timeline]
  )
  function zoomIn() {
    // Zoom around the playhead when it is inside the view, else around the
    // view center. Computed lazily: the playhead lives in state declared below.
    const anchor = state.clock >= viewBounds.start && state.clock <= viewBounds.end
      ? state.clock
      : (viewBounds.start + viewBounds.end) / 2
    setView(zoomView(viewBounds.start, viewBounds.end, anchor, range))
  }
  function zoomOut() {
    const span = viewBounds.end - viewBounds.start
    const center = (viewBounds.start + viewBounds.end) / 2
    const start = Math.max(range.start, center - span)
    const end = Math.min(range.end, center + span)
    setView(start <= range.start && end >= range.end ? null : { start, end })
  }
  function pan(direction: -1 | 1) {
    const shift = (direction * (viewBounds.end - viewBounds.start)) / 3
    const start = Math.max(range.start, viewBounds.start + shift)
    const end = Math.min(range.end, viewBounds.end + shift)
    setView({ start, end })
  }
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
  }
  // The timeline band element. Built once per render; the placement below
  // decides whether it lives inside the console (plain mode) or spans the
  // full workspace width as row 2 (workspace mode).
  const timelineBand = (
    <Timeline
      timeline={viewTimeline}
      zone={zone}
      clock={state.clock}
      selected={selected}
      viewIsFull={!view}
      pickMode={pickMode}
      bracket={pickMode ? bracket : null}
      channelID={channelID}
      fullRange={range}
      viewBounds={viewBounds}
      fullDensity={fullDensity}
      onZoomAt={zoomAt}
      onMoveView={moveViewTo}
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
      onSelect={setSelected}
      onSeek={(at) => {
        const snap = snapToPlayable(timeline, at)
        controller.current?.seek(snap.at)
      }}
      onPlay={(entry) => {
        setSelected(entry)
        controller.current?.start(entry.start)
      }}
    />
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
              // Fixed rail/files widths so long content (retention copy,
              // channel names) can never stretch the columns; only the
              // console flexes.
              gridTemplateColumns: '200px minmax(0, 1fr) 340px',
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
          className='h-8 border-video-border bg-white/5 text-video-chrome-foreground placeholder:text-video-chrome-foreground/40'
        />
        <div className='grid min-h-0 flex-1 content-start gap-0.5 overflow-y-auto max-lg:grid-flow-col max-lg:auto-cols-max max-lg:overflow-x-auto'>
          {filtered.map((channel) => (
            <Button
              key={channel.id}
              variant='ghost'
              onClick={() => selectChannel(channel)}
              className={cn(
                'h-8 w-full justify-start truncate px-2 text-left text-sm font-normal text-video-chrome-foreground hover:bg-white/10 hover:text-video-chrome-foreground',
                channel.id === channelID && 'bg-white/15 font-medium'
              )}
            >
              {label(channel)}
            </Button>
          ))}
        </div>
        <Button
          asChild
          variant='ghost'
          className='h-8 justify-start px-2 text-xs text-video-chrome-foreground/70 hover:bg-white/10 hover:text-video-chrome-foreground'
        >
          <Link to='/live'>返回实时预览</Link>
        </Button>
      </aside>
      )}
      <section className='flex min-h-0 min-w-0 flex-1 flex-col gap-2'>
        {/* The console: search rides INSIDE the dark surface as one slim
            toolbar — dates, search, quick ranges, no visible labels. */}
        <div data-testid='playback-console' className='flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border border-video-border bg-video-surface text-video-chrome-foreground shadow-sm'>
          <form
            className='flex flex-wrap items-center gap-1.5 border-b border-video-border px-3 py-2'
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
              className='h-8 w-52 border-video-border bg-white/5 text-video-chrome-foreground'
              required
            />
            <span aria-hidden className='text-video-chrome-foreground/40'>→</span>
            <Input
              type='datetime-local'
              step='60'
              aria-label='检索结束时间'
              value={draft.end}
              onChange={(event) =>
                setDraft({ ...draft, end: event.target.value })
              }
              className='h-8 w-52 border-video-border bg-white/5 text-video-chrome-foreground'
              required
            />
            <Button type='submit' size='sm' className='h-8'>检索录像</Button>
            <div className='flex items-center gap-1'>
              {QUICK_RANGES.map(({ kind, label: name }) => (
                <Button
                  key={kind}
                  type='button'
                  size='sm'
                  variant='ghost'
                  className='h-8 px-2 text-xs text-video-chrome-foreground/80 hover:bg-white/10 hover:text-video-chrome-foreground'
                  onClick={() => applyRange(quickRange(kind, Date.now(), zone))}
                >
                  {name}
                </Button>
              ))}
            </div>
            {rangeError && (
              <p role='alert' className='w-full text-sm text-red-400'>
                {rangeError}
              </p>
            )}
          </form>
          <div
            ref={frame}
            className='group/frame relative flex min-h-[260px] flex-1 items-center justify-center overflow-hidden bg-black'
          >
            <video
              ref={video}
              data-testid='playback-video'
              playsInline
              className='absolute inset-0 h-full w-full bg-black object-contain'
            />
            {/* Top-OSD metadata strip: channel + media facts the operator reads
                at a glance, the same way the live feature does. Backed by
                the segment currently loaded for playback; shows the channel
                pill alone when idle. */}
            <div
              data-testid='playback-frame-osd'
              className='pointer-events-none absolute inset-x-0 top-0 flex flex-wrap items-center gap-1.5 p-3'
            >
              {active && (
                <span className='rounded-md border border-white/15 bg-black/55 px-2 py-0.5 text-[11px] text-white shadow-sm backdrop-blur'>
                  CH{String(active.channel_no).padStart(2, '0')} {active.channel_name}
                </span>
              )}
              {state.entry?.media && state.entry.media.width > 0 && (
                <span className='rounded-md border border-white/15 bg-black/55 px-2 py-0.5 text-[11px] tabular-nums text-white/85 shadow-sm backdrop-blur'>
                  {state.entry.media.width}×{state.entry.media.height}
                </span>
              )}
              {state.entry?.media?.fps ? (
                <span className='rounded-md border border-white/15 bg-black/55 px-2 py-0.5 text-[11px] tabular-nums text-white/85 shadow-sm backdrop-blur'>
                  {Math.round(state.entry.media.fps)}fps
                </span>
              ) : null}
              {state.entry?.media?.codec ? (
                <span className='rounded-md border border-white/15 bg-black/55 px-2 py-0.5 text-[11px] uppercase text-white/85 shadow-sm backdrop-blur'>
                  {state.entry.media.codec}
                </span>
              ) : null}
              {state.entry && (
                <span
                  title='本回放不经转码'
                  className='rounded-md border border-emerald-400/40 bg-emerald-400/15 px-2 py-0.5 text-[11px] text-emerald-300 shadow-sm backdrop-blur'
                >
                  直通
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
                  'pointer-events-none absolute right-3 top-3 flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-[11px] tabular-nums shadow-sm backdrop-blur',
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
            {/* Idle placeholder: a clear "waiting for the operator" cue lives
                inside the same frame as the video, so the OSD bar below it
                always reads as a control bar over a real picture rather than
                floating above empty black. */}
            {state.phase === 'idle' && !state.entry && (
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
                'absolute inset-x-0 bottom-0 flex flex-wrap items-center gap-1.5 bg-gradient-to-t from-black/85 via-black/60 to-transparent px-3 pb-2.5 pt-8 transition-opacity duration-200',
                state.phase === 'playing'
                  ? 'opacity-0 group-hover/frame:opacity-100 focus-within:opacity-100'
                  : 'opacity-100'
              )}
            >
              <Button
                size='icon'
                variant='ghost'
                className='size-8 text-white hover:bg-white/15 hover:text-white'
                aria-label={state.phase === 'playing' ? '暂停' : '播放'}
                title={state.phase === 'playing' ? '暂停' : '播放'}
                onClick={() =>
                  state.phase === 'playing'
                    ? controller.current?.pause()
                    : controller.current?.resume()
                }
                disabled={!timeline.entries.length}
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
                className='size-8 text-white hover:bg-white/15 hover:text-white'
                aria-label='-10 秒'
                title='-10 秒'
                onClick={() => controller.current?.shift(-10)}
              >
                <RotateCcw size={16} />
              </Button>
              <Button
                size='icon'
                variant='ghost'
                className='size-8 text-white hover:bg-white/15 hover:text-white'
                aria-label='+10 秒'
                title='+10 秒'
                onClick={() => controller.current?.shift(10)}
              >
                <RotateCcw size={16} className='scale-x-[-1]' />
              </Button>
              <Button
                size='icon'
                variant='ghost'
                className='size-8 text-white hover:bg-white/15 hover:text-white'
                aria-label='下一可用片段'
                title='下一可用片段'
                onClick={() => controller.current?.jumpToNext()}
              >
                <SkipForward size={16} />
              </Button>
              <Button
                size='icon'
                variant='ghost'
                className='size-8 text-white hover:bg-white/15 hover:text-white'
                aria-label='停止'
                title='停止'
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
                className='size-8 text-white hover:bg-white/15 hover:text-white'
                aria-label='放大'
                title='放大'
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
                  options={RATES.map((rate) => ({
                    value: String(rate),
                    label: `${rate}x`,
                  }))}
                />
              </div>
              <Button
                size='sm'
                variant='ghost'
                className='ml-auto h-8 text-white hover:bg-white/15 hover:text-white'
                aria-label={
                  selected
                    ? selected.blocked
                      ? '从下一可用片段开始播放'
                      : '从此片段开始播放'
                    : '播放首个可用片段'
                }
                onClick={() =>
                  startTarget && controller.current?.start(startTarget.start)
                }
                disabled={!startTarget}
              >
                <Play size={14} />
                {selected
                  ? selected.blocked
                    ? '下一可用'
                    : '播放此段'
                  : '播放'}
              </Button>
            </div>
          </div>
          {/* Status strip: one quiet line between the picture and the
              timeline — phase, position, and any warning, no card walls. */}
          <div className='flex flex-wrap items-center gap-x-4 gap-y-0.5 border-t border-video-border px-3 py-1.5 text-[11px] text-video-chrome-foreground/70'>
            <p role='status'>
              {state.message}
              <span className='mx-2 text-video-chrome-foreground/30'>·</span>
              当前位置{' '}
              {state.clock > 0
                ? formatClock(state.clock, zone)
                : '—'}
              {state.entry ? ` · 片段 ${state.entry.id.slice(0, 8)}` : ''}
            </p>
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
                  className='h-6 border-white/20 px-2 text-xs text-white hover:bg-white/10'
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
          {/* Export bracket + zoom controls ride the console, right above the
              timeline they act on. */}
          <div className='flex flex-wrap items-center gap-2 border-t border-video-border px-3 py-1.5'>
            <Button
              size='sm'
              variant={pickMode ? 'secondary' : 'ghost'}
              className='h-7 px-2 text-xs text-video-chrome-foreground hover:bg-white/10'
              aria-pressed={pickMode}
              onClick={() => {
                setPickMode(!pickMode)
                setBracket(null)
              }}
            >
              选择导出区间
            </Button>
            {pickMode && bracket && bracket.end > bracket.start && (
              <>
                <Button
                  size='sm'
                  className='h-7 px-2 text-xs'
                  onClick={() => setExportRange({ ...bracket })}
                >
                  导出所选区间
                </Button>
                <Button
                  size='sm'
                  variant='ghost'
                  className='h-7 px-2 text-xs text-video-chrome-foreground hover:bg-white/10'
                  onClick={() => setBracket(null)}
                >
                  清除区间
                </Button>
              </>
            )}
            {pickMode && !bracket && (
              <span className='text-xs text-video-chrome-foreground/60'>
                在时间轴空白处点两次：第一次定起点，第二次定终点。
              </span>
            )}
            <div className='ml-auto flex items-center gap-0.5'>
              <Button size='sm' variant='ghost' aria-label='时间轴左移' disabled={!view} onClick={() => pan(-1)}
                className='h-7 w-7 p-0 text-video-chrome-foreground hover:bg-white/10 hover:text-video-chrome-foreground'>
                <ChevronLeft size={14} />
              </Button>
              <Button size='sm' variant='ghost' aria-label='时间轴放大' disabled={viewBounds.end - viewBounds.start <= 60_000} onClick={zoomIn}
                className='h-7 w-7 p-0 text-video-chrome-foreground hover:bg-white/10 hover:text-video-chrome-foreground'>
                <ZoomIn size={14} />
              </Button>
              <Button size='sm' variant='ghost' aria-label='时间轴缩小' disabled={!view} onClick={zoomOut}
                className='h-7 w-7 p-0 text-video-chrome-foreground hover:bg-white/10 hover:text-video-chrome-foreground'>
                <ZoomOut size={14} />
              </Button>
              <Button size='sm' variant='ghost' aria-label='时间轴右移' disabled={!view} onClick={() => pan(1)}
                className='h-7 w-7 p-0 text-video-chrome-foreground hover:bg-white/10 hover:text-video-chrome-foreground'>
                <ChevronRight size={14} />
              </Button>
              <Button size='sm' variant='ghost' aria-label='显示完整范围' disabled={!view} onClick={() => setView(null)}
                className='h-7 w-7 p-0 text-video-chrome-foreground hover:bg-white/10 hover:text-video-chrome-foreground'>
                <Maximize2 size={14} />
              </Button>
            </div>
          </div>
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
        {data && !timeline.entries.length && (
          <Empty>该时间范围没有已登记录像</Empty>
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
        {selected && (
          <div className='flex flex-wrap items-center justify-between gap-2 rounded-xl border bg-card px-3 py-2 text-sm'>
            <p>
              <span className='font-medium'>
                片段 {selected.id.slice(0, 8)} · {selected.blocked || '可用'}
              </span>
              <span className='ml-3 text-muted-foreground'>
                {formatClock(selected.start, zone)} —{' '}
                {formatClock(selected.end, zone)} ·{' '}
                {formatDuration(selected.end - selected.start)} ·{' '}
                {(selected.bytes / 1048576).toFixed(1)} MiB
              </span>
            </p>
            {active.permissions.includes('export') && !selected.blocked && (
              <Button size='sm' variant='outline' onClick={() => setExportOpen(true)}>
                导出
              </Button>
            )}
          </div>
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
        <p className='px-1 text-[11px] text-muted-foreground'>
          当前通道 {label(active)}
          。缺口不会静默跳过；暂停、拖动、±10 秒与倍速都在同一条时间轴上生效。
        </p>
      </section>
      {/* Workspace mode extras: the files slot rides column 3 of row 1, and
          the timeline band spans the full width as row 2. */}
      {layout?.kind === 'workspace' && (
        <>
          {layout.files}
          <div style={{ gridColumn: '1 / -1' }}>{timelineBand}</div>
        </>
      )}
    </div>
  )
}

function Timeline({
  timeline,
  zone,
  clock,
  selected,
  viewIsFull,
  pickMode,
  bracket,
  channelID,
  fullRange,
  viewBounds,
  fullDensity,
  onZoomAt,
  onMoveView,
  onPick,
  onSelect,
  onSeek,
  onPlay,
}: {
  timeline: Timeline
  zone: string
  clock: number
  selected: Entry | null
  viewIsFull: boolean
  pickMode: boolean
  bracket: { start: number; end: number } | null
  channelID: string
  fullRange: { start: number; end: number }
  viewBounds: { start: number; end: number }
  fullDensity: ReturnType<typeof coverageDensity>
  onZoomAt: (anchor: number, factor: number) => void
  onMoveView: (center: number) => void
  onPick: (at: number) => void
  onSelect: (entry: Entry) => void
  onSeek: (at: number) => void
  onPlay: (entry: Entry) => void
}) {
  const strip = useRef<HTMLDivElement>(null)
  const bubble = useRef<HTMLDivElement>(null)
  const [scrubbing, setScrubbing] = useState(false)
  const [stripWidth, setStripWidth] = useState(0)
  const span = timeline.to - timeline.from
  // The ruler adapts to the strip's real width, so the same view reads
  // differently on a laptop and a fullscreen monitor.
  useEffect(() => {
    const element = strip.current
    if (!element) return
    const update = () => setStripWidth(element.getBoundingClientRect().width)
    update()
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])
  const ticks = useMemo(
    () => buildTicks(timeline.from, timeline.to, stripWidth, zone),
    [timeline.from, timeline.to, stripWidth, zone]
  )
  // Density adapts to the strip: roughly one bucket per 8px, bounded so both
  // a month view and a minute view render a smooth profile.
  const density = useMemo(
    () =>
      coverageDensity(
        timeline,
        Math.max(8, Math.min(160, Math.round(stripWidth / 8)))
      ),
    [timeline, stripWidth]
  )
  // The event track shows the events the API vouches for: the view window
  // clamped to the newest day. It is a bounded, honest query, not a full set.
  const eventWindow = useMemo(
    () => eventWindowSize(timeline.from, timeline.to),
    [timeline.from, timeline.to]
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
  const marks = useMemo(
    () => eventMarks(eventsQuery.data?.items || [], timeline.from, timeline.to),
    [eventsQuery.data, timeline.from, timeline.to]
  )
  // Pixel clustering keeps three days of events a readable scatter: marks
  // closer than 8px fold into one dot whose tooltip reports the count.
  const clusters = useMemo(
    () => clusterMarks(marks, timeline.from, timeline.to, stripWidth),
    [marks, timeline.from, timeline.to, stripWidth]
  )
  const position = (at: number) =>
    `${Math.min(Math.max(((at - timeline.from) / span) * 100, 0), 100)}%`
  function scrubTo(clientX: number) {
    const box = strip.current?.getBoundingClientRect()
    if (!box || !box.width) return
    onSeek(timeline.from + ((clientX - box.left) / box.width) * span)
  }
  function beginScrub(event: ReactPointerEvent<HTMLElement>) {
    event.currentTarget.setPointerCapture(event.pointerId)
    setScrubbing(true)
    scrubTo(event.clientX)
  }
  // The scrub preview is written straight to a DOM node, not through state:
  // a React re-render from pointermove combined with the strip's pointer
  // capture reliably drops the second click of a double click (the browser
  // retargets mid-sequence), which would break double-click-to-play.
  function trackHover(event: ReactPointerEvent<HTMLElement>) {
    const node = bubble.current
    const box = strip.current?.getBoundingClientRect()
    if (!node || !box || !box.width) return
    node.style.visibility = 'visible'
    node.style.left = `${Math.min(Math.max(event.clientX - box.left, 0), box.width)}px`
    node.textContent = formatClock(
      timeline.from + ((event.clientX - box.left) / box.width) * span,
      zone
    )
  }
  function hideHover() {
    const node = bubble.current
    if (node) node.style.visibility = 'hidden'
  }
  // An empty index is explained inside the band itself: the full-width row
  // must still occupy its slot (the workspace grid reserves it), so the
  // empty state renders as a slim notice inside the band, not a null.
  // A zoomed view with no segments is different information — the full index
  // has content, this window simply does not.
  if (!timeline.entries.length || span <= 0) {
    if (viewIsFull) {
      return (
        <div className='rounded-xl border border-dashed border-video-border p-4 text-center text-sm text-video-chrome-foreground/60'>
          {zone ? (
            <>
              {formatClock(timeline.from, zone)} — {formatClock(timeline.to, zone)} ·
              该时间范围没有已登记录像
            </>
          ) : (
            '该时间范围没有已登记录像'
          )}
        </div>
      )
    }
    return (
      <p className='rounded-xl border border-dashed p-4 text-center text-sm text-muted-foreground'>
        该视图范围内没有片段——用「显示完整范围」返回，或调整缩放。
      </p>
    )
  }
  // Wheel zoom anchored under the pointer; shift+wheel pans like a map.
  function onWheel(event: ReactWheelEvent<HTMLElement>) {
    event.preventDefault()
    const box = strip.current?.getBoundingClientRect()
    if (!box || !box.width) return
    const at = timeline.from + ((event.clientX - box.left) / box.width) * span
    if (event.shiftKey) {
      const direction = event.deltaY > 0 ? 1 : -1
      onMoveView(at + direction * span * 0.25)
    } else {
      onZoomAt(at, event.deltaY > 0 ? 1.4 : 1 / 1.4)
    }
  }
  const fullSpan = fullRange.end - fullRange.start
  function minimapPointer(event: ReactPointerEvent<HTMLElement>) {
    const box = event.currentTarget.getBoundingClientRect()
    if (!box.width || fullSpan <= 0) return
    onMoveView(fullRange.start + ((event.clientX - box.left) / box.width) * fullSpan)
  }
  const band = (
    <div className='flex flex-col gap-1.5 rounded-xl border border-video-border bg-video-surface px-3.5 py-2.5'>
      {/* Band 1 — axis header: density read on the left, the playhead clock on
          the right (the sample's `tl-axis` row). */}
      <div className='flex items-center justify-between gap-3 text-[11px] tabular-nums text-video-chrome-foreground/50'>
        <span data-testid='playback-timeline-density'>
          密度 <b className='font-medium text-video-chrome-foreground'>{timeline.entries.length}</b> 段
          {timeline.gaps.length > 0 && (
            <span className='ml-1.5 text-red-400'>缺口 {timeline.gaps.length}</span>
          )}
        </span>
        <span data-testid='playback-timeline-window' className='text-video-chrome-foreground/60'>
          {formatClock(timeline.from, zone)} — {formatClock(timeline.to, zone)}
        </span>
        <span>
          当前{' '}
          <b data-testid='playback-timeline-clock' className='font-medium text-red-400'>
            {clock > 0 ? formatClock(clock, zone) : '—'}
          </b>
        </span>
      </div>
      {/* Band 2 — density strip. The whole search range's coverage (minimap
          style) with the zoom viewport outlined; dragging slides the view. */}
      <div
        aria-hidden
        data-testid='playback-minimap'
        className='relative h-6 cursor-pointer overflow-hidden rounded-md bg-white/[0.04] shadow-[inset_0_0_0_1px_var(--video-border,var(--border))]'
        onPointerDown={(event) => {
          event.currentTarget.setPointerCapture(event.pointerId)
          minimapPointer(event)
        }}
        onPointerMove={(event) => {
          if (event.buttons & 1) minimapPointer(event)
        }}
      >
        {fullDensity.map((bucket, index) => {
          const ratio = bucket.to > bucket.from ? bucket.coveredMs / (bucket.to - bucket.from) : 0
          if (ratio <= 0) return null
          const alpha = `${Math.min(0.95, 0.5 + ratio * 0.45)}`
          return (
            <div
              key={index}
              className='absolute bottom-0 top-0 border-r border-black/30'
              style={{
                left: `${((bucket.from - fullRange.start) / fullSpan) * 100}%`,
                width: `calc(${((bucket.to - fullRange.start) / fullSpan) * 100}% - ${((bucket.from - fullRange.start) / fullSpan) * 100}%)`,
                minWidth: '2px',
                background: `linear-gradient(to top, oklch(0.55 0.13 70 / 80%), oklch(0.75 0.13 70 / ${alpha}))`,
              }}
            />
          )
        })}
        {!viewIsFull && (
          <div
            className='absolute bottom-0 top-0 border-x-2 border-amber-400 bg-amber-400/10'
            style={{
              left: `${((viewBounds.start - fullRange.start) / fullSpan) * 100}%`,
              width: `${((viewBounds.end - viewBounds.start) / fullSpan) * 100}%`,
            }}
          />
        )}
      </div>
      {/* Band 3 — event rail. Slim 8px track; open events carry a halo. */}
      <div
        aria-hidden
        data-testid='playback-events-track'
        className='relative h-2'
      >
        {clusters.map((mark) => (
          <span
            key={mark.id}
            title={`${markLabel(mark.category)}${mark.count > 1 ? ` ×${mark.count}` : ''} · ${formatClock(mark.at, zone)} · ${Math.round(mark.score * 100)}%`}
            className={cn(
              'absolute top-1/2 w-[2px] -translate-x-1/2 -translate-y-1/2 rounded-full',
              mark.handleState === 'open'
                ? 'h-full bg-amber-400 shadow-[0_0_5px_rgba(251,191,36,0.6)]'
                : 'h-1/2 bg-sky-400/80'
            )}
            style={{ left: position(mark.at) }}
          />
        ))}
      </div>
      {/* Band 4 — the interactive strip: segments, bracket, ticks and the
          playhead. Same behaviours as before (click select, double-click
          play, drag scrub, wheel zoom), now at full width. */}
      <div
        ref={strip}
        data-testid='playback-timeline'
        aria-label='录像时间轴'
        className={`relative h-10 touch-none rounded-md bg-white/[0.04] shadow-[inset_0_0_0_1px_var(--video-border,var(--border))] ${
          scrubbing ? 'cursor-grabbing' : 'cursor-crosshair'
        }`}
        onPointerDown={(event) => {
          // Only the empty background positions playback (or picks an export
          // boundary in picker mode); a bar click selects.
          if (event.target !== event.currentTarget) return
          if (pickMode) {
            onPick(timeline.from + ((event.clientX - event.currentTarget.getBoundingClientRect().left) / event.currentTarget.getBoundingClientRect().width) * span)
            return
          }
          beginScrub(event)
        }}
        onPointerMove={(event) => {
          if (pickMode || scrubbing) return
          trackHover(event)
        }}
        onPointerLeave={hideHover}
        onPointerUp={() => setScrubbing(false)}
        onWheel={onWheel}
      >
        <div aria-hidden className='pointer-events-none absolute inset-x-0 bottom-0 top-0'>
          {density.map((bucket, index) => {
            const ratio = bucket.to > bucket.from ? bucket.coveredMs / (bucket.to - bucket.from) : 0
            if (ratio <= 0) return null
            const top = `${Math.min(0.92, 0.30 + ratio * 0.6)}`
            return (
              <div
                key={index}
                className='absolute bottom-0'
                style={{
                  left: position(bucket.from),
                  width: `calc(${position(bucket.to)} - ${position(bucket.from)})`,
                  height: `${Math.max(ratio * 100, 4)}%`,
                  background: `linear-gradient(to top, oklch(0.55 0.13 70 / 35%), oklch(0.78 0.13 75 / ${top}))`,
                }}
              />
            )
          })}
        </div>
        <div
          ref={bubble}
          aria-hidden
          className='pointer-events-none absolute -top-8 z-10 -translate-x-1/2 whitespace-nowrap rounded-md border bg-popover px-2 py-1 text-xs text-popover-foreground shadow-md'
          style={{ visibility: 'hidden', left: 0 }}
        />
        {timeline.entries.map((entry) => (
          <Button
            key={entry.id}
            variant='ghost'
            title={`${formatClock(entry.start, zone)} — ${formatClock(entry.end, zone)}${
              entry.blocked ? ` · ${entry.blocked}` : ''
            }`}
            aria-label={`片段 ${formatClock(entry.start, zone)}`}
            onClick={() => onSelect(entry)}
            onDoubleClick={() => onPlay(entry)}
            className={cn(
              'absolute top-1 h-8 rounded p-0 transition-colors duration-150',
              entry.blocked
                ? 'bg-amber-500/40 hover:bg-amber-500/60'
                : entry.id === selected?.id
                  ? 'bg-primary'
                  : 'bg-emerald-600/70 hover:bg-emerald-500/80'
            )}
            style={{
              left: position(entry.start),
              width: position(entry.end),
              minWidth: '3px',
            }}
          />
        ))}
        {bracket && bracket.end > bracket.start && (
          <div
            data-testid='playback-bracket'
            aria-label='导出区间'
            className='pointer-events-none absolute top-0 bottom-0 border-x-2 border-amber-400'
            style={{
              left: position(bracket.start),
              width: `calc(${position(bracket.end)} - ${position(bracket.start)})`,
              background:
                'repeating-linear-gradient(45deg, oklch(0.78 0.13 75 / 18%) 0 6px, transparent 6px 12px)',
            }}
          >
            <span className='absolute -top-6 left-1/2 -translate-x-1/2 whitespace-nowrap rounded border border-amber-400/60 bg-amber-400 px-2 py-0.5 text-[10.5px] font-semibold text-amber-950 shadow-md'>
              选区 · {formatDuration(bracket.end - bracket.start)} · 用于导出
            </span>
          </div>
        )}
        {ticks.map((tick) => (
          <div
            key={tick.at}
            aria-hidden
            className={`pointer-events-none absolute w-px ${
              tick.major
                ? 'top-0 h-full bg-amber-300/70'
                : 'top-2 h-1/2 bg-amber-300/30'
            }`}
            style={{ left: position(tick.at) }}
          />
        ))}
        {clock > 0 && (
          <div
            data-testid='playback-playhead'
            className='absolute top-0 bottom-0 -ml-2 w-4 cursor-grab active:cursor-grabbing'
            style={{ left: position(clock) }}
            onPointerDown={beginScrub}
            onPointerMove={(event) => scrubbing && scrubTo(event.clientX)}
            onPointerUp={() => setScrubbing(false)}
          >
            <div className='relative mx-auto h-full w-0.5 bg-red-500 shadow-[0_0_8px_rgba(239,68,68,0.7)]'>
              <div
                aria-hidden
                className='absolute -top-0.5 left-1/2 h-2.5 w-2.5 -translate-x-1/2 rounded-sm bg-red-500'
                style={{ clipPath: 'polygon(50% 100%, 0 0, 100% 0)' }}
              />
            </div>
          </div>
        )}
      </div>
      {/* Band 5 — the ruler: amber tick labels under the strip, edge labels
          flip inward instead of half-vanishing. */}
      {ticks.length > 0 && (
        <div aria-hidden className='relative h-4 select-none text-[10px] leading-4'>
          {ticks.map((tick, index) => {
            const first = index === 0
            const last = index === ticks.length - 1
            return (
              <span
                key={tick.at}
                className={`absolute whitespace-nowrap tabular-nums ${
                  tick.major
                    ? 'font-medium text-video-chrome-foreground'
                    : 'text-video-chrome-foreground/50'
                }`}
                style={{
                  left: position(tick.at),
                  transform: first
                    ? 'none'
                    : last
                      ? 'translateX(-100%)'
                      : 'translateX(-50%)',
                }}
              >
                {tick.label}
              </span>
            )
          })}
        </div>
      )}
      {/* Band 6 — legend + keyboard cheatsheet, the sample's `tl-foot`. */}
      <div className='flex flex-wrap items-center gap-x-3.5 gap-y-1 text-[11px] text-video-chrome-foreground/50'>
        <span className='inline-flex items-center gap-1.5'>
          <span className='size-2 rounded-sm bg-emerald-600' />
          可用片段
        </span>
        <span className='inline-flex items-center gap-1.5'>
          <span className='size-2 rounded-sm bg-amber-500/60' />
          受阻片段
        </span>
        <span className='inline-flex items-center gap-1.5'>
          <span className='size-2 rounded-sm bg-amber-400' />
          事件（琥珀=待处理）
        </span>
        <span className='inline-flex items-center gap-1.5'>
          <span className='size-2 rounded-sm bg-red-500' />
          播放位置
        </span>
        <div className='ml-auto flex flex-wrap items-center gap-2.5'>
          <span><kbd className='rounded border border-video-border bg-white/10 px-1 py-px text-[10px] text-video-chrome-foreground/80'>Space</kbd> 播放/暂停</span>
          <span><kbd className='rounded border border-video-border bg-white/10 px-1 py-px text-[10px] text-video-chrome-foreground/80'>←</kbd>/<kbd className='rounded border border-video-border bg-white/10 px-1 py-px text-[10px] text-video-chrome-foreground/80'>→</kbd> 跳 10 秒</span>
          <span><kbd className='rounded border border-video-border bg-white/10 px-1 py-px text-[10px] text-video-chrome-foreground/80'>滚轮</kbd> 缩放</span>
          <span><kbd className='rounded border border-video-border bg-white/10 px-1 py-px text-[10px] text-video-chrome-foreground/80'>双击</kbd> 播放</span>
        </div>
      </div>
    </div>
  )
  // The workspace shell owns a full-width row under the three columns; the
  // band portals there so the timeline reads as one band across the whole
  // workspace, exactly like the sample layout. Falls back to inline when no
  // host id was given (standalone /playback-style renders, tests).
  return band
}
