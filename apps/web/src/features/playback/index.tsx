import { useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
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
  zoomView,
  type Entry,
  type Timeline,
} from './timeline'
import { eventMarks, eventWindowSize, markLabel } from './event-marks'
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
  embedded = false,
  active = true,
}: {
  initialChannel?: string
  initialAt?: string
  // Embedded under the recordings workspace's playback tab, which already
  // renders the page heading and the tab strip.
  embedded?: boolean
  // False while the workspace shows the other tab: the channel/site queries
  // (and the index below) stay idle instead of both tabs fetching on mount.
  active?: boolean
}) {
  const channelsQuery = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100',
    active
  )
  const site = useAPI<Schema<'Site'>>('/api/v1/site', active)
  const channels = (channelsQuery.data?.items || []).filter((channel) =>
    channel.permissions.includes('playback')
  )
  // The site zone is never assumed: every clock in this page is derived from it,
  // so the workspace waits for the value the API reports.
  const zone = site.data?.timezone || ''
  return (
    <div className='space-y-4'>
      {!embedded && (
        <div>
          <h1 className='text-2xl font-semibold tracking-tight'>录像回放</h1>
          <p className='mt-1 text-sm text-muted-foreground'>
            {zone
              ? `按站点时区 ${zone} 检索与播放。进入本页只加载索引，播放由播放按钮、双击片段或时间轴定位触发。`
              : '正在读取站点时区…'}
          </p>
        </div>
      )}
      {embedded && (
        <p className='text-xs text-muted-foreground'>
          {zone
            ? `按站点时区 ${zone} 检索与播放；播放由播放按钮、双击片段或时间轴定位触发。`
            : '正在读取站点时区…'}
        </p>
      )}
      <QueryState
        pending={channelsQuery.isPending || site.isPending}
        error={channelsQuery.error || site.error}
        retry={() => {
          void channelsQuery.refetch()
          void site.refetch()
        }}
      />
      {!!zone && channelsQuery.data && !channels.length && (
        <Empty>没有可回放的通道，请联系管理员分配录像回放权限。</Empty>
      )}
      {!!zone && !!channels.length && (
        <Workspace
          // A site timezone change invalidates every wall-clock value below.
          key={zone}
          channels={channels}
          zone={zone}
          initialChannel={initialChannel}
          initialAt={initialAt}
          isActive={active}
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
}: {
  channels: Schema<'Channel'>[]
  zone: string
  initialChannel: string
  initialAt: string
  isActive?: boolean
}) {
  const [channelID, setChannelID] = useState(
    () =>
      channels.find((channel) => channel.id === initialChannel)?.id ||
      channels[0].id
  )
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
  return (
    <div className='grid items-start gap-4 xl:grid-cols-[240px_minmax(0,1fr)]'>
      <aside className='space-y-3 rounded-xl border bg-card p-3'>
        <div className='flex items-center justify-between text-sm font-medium'>
          <span>回放通道</span>
          <span className='text-muted-foreground'>{channels.length} 路</span>
        </div>
        <Input
          aria-label='搜索回放通道'
          placeholder='搜索编号或名称'
          value={search}
          onChange={(event) => setSearch(event.target.value)}
        />
        <div className='grid max-h-80 gap-1 overflow-y-auto'>
          {filtered.map((channel) => (
            <Button
              key={channel.id}
              variant='ghost'
              onClick={() => {
                setChannelID(channel.id)
                setSelected(null)
                setView(null)
                setBracket(null)
              }}
              className={cn(
                'h-auto w-full justify-start truncate px-2 py-2 text-left text-sm font-normal',
                channel.id === channelID && 'bg-accent font-medium'
              )}
            >
              {label(channel)}
            </Button>
          ))}
        </div>
        <p className='text-xs text-muted-foreground'>
          只有已完成发布、校验通过的片段可播放。缺口不会被静默跳过。
        </p>
        <Button asChild variant='link' className='h-auto p-0'>
          <Link to='/live'>返回实时预览</Link>
        </Button>
      </aside>
      <section className='space-y-3'>
        {/* The console: one dark surface holds the picture, its status line and
            the timeline, so the operator's eyes never leave the video. Only
            the search toolbar above stays on the page background. */}
        <form
          className='flex flex-wrap items-end gap-2'
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
          <label className='grid gap-1 text-sm'>
            <span className='text-xs text-muted-foreground'>开始（{zone}）</span>
            <Input
              type='datetime-local'
              step='1'
              aria-label='检索开始时间'
              value={draft.start}
              onChange={(event) =>
                setDraft({ ...draft, start: event.target.value })
              }
              className='w-56'
              required
            />
          </label>
          <label className='grid gap-1 text-sm'>
            <span className='text-xs text-muted-foreground'>结束（{zone}）</span>
            <Input
              type='datetime-local'
              step='1'
              aria-label='检索结束时间'
              value={draft.end}
              onChange={(event) =>
                setDraft({ ...draft, end: event.target.value })
              }
              className='w-56'
              required
            />
          </label>
          <Button type='submit' className='mb-0.5'>检索录像</Button>
          <div className='mb-0.5 flex flex-wrap items-center gap-1.5'>
            {QUICK_RANGES.map(({ kind, label: name }) => (
              <Button
                key={kind}
                type='button'
                size='sm'
                variant='outline'
                onClick={() => applyRange(quickRange(kind, Date.now(), zone))}
              >
                {name}
              </Button>
            ))}
          </div>
          {rangeError && (
            <p role='alert' className='mb-1 w-full text-sm text-destructive'>
              {rangeError}
            </p>
          )}
        </form>
        <div className='overflow-hidden rounded-xl border bg-video-surface text-video-chrome-foreground shadow-sm'>
          <div ref={frame} className='group/frame relative'>
            <video
              ref={video}
              data-testid='playback-video'
              playsInline
              className='aspect-video w-full bg-black'
            />
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
                size='sm'
                variant='ghost'
                className='text-white hover:bg-white/15 hover:text-white'
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
                {state.phase === 'playing' ? '暂停' : '播放'}
              </Button>
              <Button
                size='sm'
                variant='ghost'
                className='text-white hover:bg-white/15 hover:text-white'
                onClick={() => controller.current?.shift(-10)}
              >
                <RotateCcw size={16} />
                -10 秒
              </Button>
              <Button
                size='sm'
                variant='ghost'
                className='text-white hover:bg-white/15 hover:text-white'
                onClick={() => controller.current?.shift(10)}
              >
                <RotateCcw size={16} className='scale-x-[-1]' />
                +10 秒
              </Button>
              <Button
                size='sm'
                variant='ghost'
                className='text-white hover:bg-white/15 hover:text-white'
                onClick={() => controller.current?.jumpToNext()}
              >
                <SkipForward size={16} />
                下一可用片段
              </Button>
              <Button
                size='sm'
                variant='ghost'
                className='text-white hover:bg-white/15 hover:text-white'
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
                停止
              </Button>
              <Button
                size='sm'
                variant='ghost'
                className='text-white hover:bg-white/15 hover:text-white'
                onClick={() =>
                  void frame.current?.requestFullscreen().catch(() => {})
                }
              >
                <Maximize size={16} />
                放大
              </Button>
              <div className='flex items-center gap-2 text-xs text-white/90'>
                <span>倍速</span>
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
                className='ml-auto text-white hover:bg-white/15 hover:text-white'
                onClick={() =>
                  startTarget && controller.current?.start(startTarget.start)
                }
                disabled={!startTarget}
              >
                {selected
                  ? selected.blocked
                    ? '从下一可用片段开始播放'
                    : '从此片段开始播放'
                  : '播放首个可用片段'}
              </Button>
            </div>
          </div>
          {/* Status strip between the picture and the timeline: phase message,
              position and the warnings, all on the console surface. */}
          <div className='flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-video-border px-3 py-2 text-xs'>
            <p role='status' className='font-medium'>
              {state.message}
            </p>
            <p className='text-video-chrome-foreground/70'>
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
          <Timeline
            timeline={viewTimeline}
            zone={zone}
            clock={state.clock}
            selected={selected}
            viewIsFull={!view}
            pickMode={pickMode}
            bracket={pickMode ? bracket : null}
            channelID={channelID}
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
            onSeek={(at) => controller.current?.seek(at)}
            onPlay={(entry) => {
              setSelected(entry)
              controller.current?.start(entry.start)
            }}
          />
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
        <p className='text-xs text-muted-foreground'>
          当前通道 {label(active)}
          。暂停、拖动、±10 秒与倍速都在同一条时间轴上生效。
        </p>
      </section>
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
  // An empty index is explained once, by the page-level empty state below the
  // timeline. A zoomed view with no segments in it is different information —
  // the full index has content, this window simply does not.
  if (!timeline.entries.length || span <= 0) {
    if (viewIsFull) return null
    return (
      <p className='rounded-xl border border-dashed p-4 text-center text-sm text-muted-foreground'>
        该视图范围内没有片段——用「显示完整范围」返回，或调整缩放。
      </p>
    )
  }
  return (
    <div className='space-y-1 px-3 pb-3 pt-2'>
      {/* Event track: a slim rail above the strip. Its markers are display
          only; seeking to an event goes through the strip below. */}
      {marks.length > 0 && (
        <div
          aria-hidden
          data-testid='playback-events-track'
          className='relative h-3 rounded-md bg-white/5'
        >
          {marks.map((mark) => (
            <span
              key={mark.id}
              title={`${markLabel(mark.category)} · ${formatClock(mark.at, zone)} · ${Math.round(mark.score * 100)}%`}
              className={cn(
                'absolute top-1/2 size-1.5 -translate-x-1/2 -translate-y-1/2 rounded-full',
                mark.handleState === 'open'
                  ? 'bg-amber-400'
                  : 'bg-sky-400/70'
              )}
              style={{ left: position(mark.at) }}
            />
          ))}
        </div>
      )}
      <div
        ref={strip}
        data-testid='playback-timeline'
        aria-label='录像时间轴'
        className={`relative h-12 touch-none rounded-lg border border-video-border bg-white/5 ${
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
          // While scrubbing, pointer capture redirects events to the strip and
          // the pointer is down — the bubble only tracks a free-moving pointer.
          if (pickMode || scrubbing) return
          trackHover(event)
        }}
        onPointerLeave={hideHover}
        onPointerUp={() => setScrubbing(false)}
      >
        {/* Coverage density: one bar per bucket, height = covered fraction.
            It reads as recording intensity without one DOM node per segment. */}
        <div aria-hidden className='pointer-events-none absolute inset-x-0 bottom-0 top-0'>
          {density.map((bucket, index) => {
            const ratio = bucket.to > bucket.from ? bucket.coveredMs / (bucket.to - bucket.from) : 0
            if (ratio <= 0) return null
            return (
              <div
                key={index}
                className='absolute bottom-0 bg-emerald-400/15'
                style={{
                  left: position(bucket.from),
                  width: `calc(${position(bucket.to)} - ${position(bucket.from)})`,
                  height: `${Math.max(ratio * 100, 4)}%`,
                }}
              />
            )
          })}
        </div>
        {/* Scrub preview: the target clock follows the pointer. */}
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
              'absolute top-1 h-10 rounded p-0 transition-colors duration-150',
              entry.blocked
                ? 'bg-amber-500/40 hover:bg-amber-500/60'
                : entry.id === selected?.id
                  ? 'bg-primary'
                  : 'bg-emerald-600/70 hover:bg-emerald-500/80'
            )}
            style={{
              left: position(entry.start),
              width: position(entry.end),
              // A short segment inside a long range rounds to nothing; keep it
              // visible and selectable instead of collapsing to a sliver.
              minWidth: '3px',
            }}
          />
        ))}
        {bracket && bracket.end > bracket.start && (
          <div
            data-testid='playback-bracket'
            aria-label='导出区间'
            className='pointer-events-none absolute top-0 bottom-0 border-x-2 border-sky-400 bg-sky-400/25'
            style={{ left: position(bracket.start), width: `calc(${position(bracket.end)} - ${position(bracket.start)})` }}
          />
        )}
        {ticks.map((tick) => (
          <div
            key={tick.at}
            aria-hidden
            className={`pointer-events-none absolute w-px ${tick.major ? 'top-0 h-full bg-foreground/25' : 'top-1 h-1/2 bg-foreground/15'}`}
            style={{ left: position(tick.at) }}
          />
        ))}
        {/* No playhead before the first playback: an epoch-zero position would
            clamp to the window start and read as a real position. */}
        {clock > 0 && (
          <div
            data-testid='playback-playhead'
            className='absolute top-0 bottom-0 -ml-2 w-4 cursor-grab active:cursor-grabbing'
            style={{ left: position(clock) }}
            onPointerDown={beginScrub}
            onPointerMove={(event) => scrubbing && scrubTo(event.clientX)}
            onPointerUp={() => setScrubbing(false)}
          >
            <div className='relative mx-auto h-full w-0.5 bg-red-500 shadow-[0_0_4px_rgba(239,68,68,0.6)]'>
              {/* The grab handle: a small triangle crowning the line so the
                  draggable affordance is visible, not just the hit area. */}
              <div
                aria-hidden
                className='absolute -top-0.5 left-1/2 h-2.5 w-2.5 -translate-x-1/2 rounded-sm bg-red-500'
                style={{ clipPath: 'polygon(50% 100%, 0 0, 100% 0)' }}
              />
            </div>
          </div>
        )}
      </div>
      {ticks.length > 0 && (
        <div aria-hidden className='relative h-4 select-none text-[10px] leading-4 text-muted-foreground'>
          {ticks.map((tick, index) => {
            const first = index === 0
            const last = index === ticks.length - 1
            // Edge labels flip their alignment instead of half-vanishing
            // outside the strip.
            return (
              <span
                key={tick.at}
                className={`absolute whitespace-nowrap ${tick.major ? 'font-medium' : ''}`}
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
      <p className='text-xs text-muted-foreground'>
        {formatClock(timeline.from, zone)} — {formatClock(timeline.to, zone)} ·{' '}
        {timeline.entries.length} 个片段 · {timeline.gaps.length}{' '}
        处缺口（单击片段选中，双击播放，拖动时间轴定位；空格播放/暂停，← → 快进快退 10 秒）
      </p>
    </div>
  )
}
