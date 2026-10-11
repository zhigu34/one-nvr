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
  formatDuration,
  sliceView,
  zoomView,
  type Entry,
  type Timeline,
} from './timeline'
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
}: {
  initialChannel?: string
  initialAt?: string
  // Embedded under the recordings workspace's playback tab, which already
  // renders the page heading and the tab strip.
  embedded?: boolean
}) {
  const channelsQuery = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100'
  )
  const site = useAPI<Schema<'Site'>>('/api/v1/site')
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
}: {
  channels: Schema<'Channel'>[]
  zone: string
  initialChannel: string
  initialAt: string
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
        <Link
          to='/live'
          className='block text-sm text-primary underline underline-offset-4'
        >
          返回实时预览
        </Link>
      </aside>
      <section className='space-y-4'>
        <form
          className='grid items-end gap-3 rounded-xl border bg-card p-4 md:grid-cols-3'
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
          <label className='grid gap-2 text-sm'>
            <span>检索开始时间（{zone}）</span>
            <Input
              type='datetime-local'
              step='1'
              aria-label='检索开始时间'
              value={draft.start}
              onChange={(event) =>
                setDraft({ ...draft, start: event.target.value })
              }
              required
            />
          </label>
          <label className='grid gap-2 text-sm'>
            <span>检索结束时间（{zone}）</span>
            <Input
              type='datetime-local'
              step='1'
              aria-label='检索结束时间'
              value={draft.end}
              onChange={(event) =>
                setDraft({ ...draft, end: event.target.value })
              }
              required
            />
          </label>
          <Button type='submit'>检索录像</Button>
          <div className='flex flex-wrap items-center gap-2 md:col-span-3'>
            <span className='text-xs text-muted-foreground'>快捷范围</span>
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
        </form>
        {rangeError && <p role='alert'>{rangeError}</p>}
        <p className='text-xs text-muted-foreground'>
          时间按站点时区 {zone} 填写与显示，单次检索不超过 31 天。
        </p>
        <div ref={frame} className='overflow-hidden rounded-xl border bg-black'>
          <video
            ref={video}
            data-testid='playback-video'
            playsInline
            className='aspect-video w-full bg-black'
          />
        </div>
        <div className='flex flex-wrap items-center gap-2'>
          <Button
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
            variant='outline'
            onClick={() => controller.current?.shift(-10)}
          >
            <RotateCcw size={16} />
            -10 秒
          </Button>
          <Button
            variant='outline'
            onClick={() => controller.current?.shift(10)}
          >
            <RotateCcw size={16} className='scale-x-[-1]' />
            +10 秒
          </Button>
          <Button
            variant='outline'
            onClick={() => controller.current?.jumpToNext()}
          >
            <SkipForward size={16} />
            下一可用片段
          </Button>
          <Button
            variant='outline'
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
            variant='outline'
            onClick={() =>
              void frame.current?.requestFullscreen().catch(() => {})
            }
          >
            <Maximize size={16} />
            放大
          </Button>
          <div className='flex items-center gap-2 text-sm'>
            <span>倍速</span>
            <SelectField
              ariaLabel='播放倍速'
              size='sm'
              className='w-20'
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
            variant='outline'
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
        <div className='rounded-xl border bg-card p-3 text-sm'>
          <p role='status'>{state.message}</p>
          <p className='mt-1 text-muted-foreground'>
            当前位置{' '}
            {state.clock > 0
              ? formatClock(state.clock, zone)
              : '—'}
            {state.entry ? ` · 片段 ${state.entry.id.slice(0, 8)}` : ''}
          </p>
          {state.skipped && (
            <p role='alert' className='mt-2 text-amber-600'>
              已自动跳过缺口 {formatClock(state.skipped.start, zone)} —{' '}
              {formatClock(state.skipped.end, zone)}（
              {formatDuration(state.skipped.end - state.skipped.start)}，
              {state.skipped.reason}）
            </p>
          )}
          {state.gap && (
            <div className='mt-2 text-amber-600'>
              <p>
                该时间没有可播放录像：{formatClock(state.gap.start, zone)} —{' '}
                {formatClock(state.gap.end, zone)}（{state.gap.reason}）
              </p>
              <Button
                size='sm'
                variant='outline'
                className='mt-2'
                onClick={() => controller.current?.jumpToNext()}
              >
                跳到下一可用片段
              </Button>
            </div>
          )}
          {state.phase === 'error' && (
            <p role='alert' className='mt-2 text-destructive'>
              {state.message}
            </p>
          )}
        </div>
        <QueryState
          pending={isPending}
          error={indexError}
          retry={refetch}
        />
        <div className='flex flex-wrap items-center gap-2'>
          <Button
            size='sm'
            variant={pickMode ? 'default' : 'outline'}
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
              <Button size='sm' onClick={() => setExportRange({ ...bracket })}>
                导出所选区间
              </Button>
              <Button size='sm' variant='ghost' onClick={() => setBracket(null)}>
                清除区间
              </Button>
            </>
          )}
          {pickMode && !bracket && (
            <span className='text-xs text-muted-foreground'>在时间轴空白处点两次：第一次定起点，第二次定终点。</span>
          )}
          <div className='ml-auto flex items-center gap-1'>
            <Button size='sm' variant='ghost' aria-label='时间轴左移' disabled={!view} onClick={() => pan(-1)}>
              <ChevronLeft size={16} />
            </Button>
            <Button size='sm' variant='ghost' aria-label='时间轴放大' disabled={viewBounds.end - viewBounds.start <= 60_000} onClick={zoomIn}>
              <ZoomIn size={16} />
            </Button>
            <Button size='sm' variant='ghost' aria-label='时间轴缩小' disabled={!view} onClick={zoomOut}>
              <ZoomOut size={16} />
            </Button>
            <Button size='sm' variant='ghost' aria-label='时间轴右移' disabled={!view} onClick={() => pan(1)}>
              <ChevronRight size={16} />
            </Button>
            <Button size='sm' variant='ghost' aria-label='显示完整范围' disabled={!view} onClick={() => setView(null)}>
              <Maximize2 size={16} />
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
          <div className='rounded-xl border bg-card p-3 text-sm'>
            <div className='flex items-center justify-between gap-2'>
              <p className='font-medium'>
                片段 {selected.id.slice(0, 8)} · {selected.blocked || '可用'}
              </p>
              {active.permissions.includes('export') && !selected.blocked && (
                <Button size='sm' variant='outline' onClick={() => setExportOpen(true)}>
                  导出
                </Button>
              )}
            </div>
            <p className='mt-1 text-muted-foreground'>
              {formatClock(selected.start, zone)} —{' '}
              {formatClock(selected.end, zone)} ·{' '}
              {formatDuration(selected.end - selected.start)} ·{' '}
              {(selected.bytes / 1048576).toFixed(1)} MiB
            </p>
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
  onPick: (at: number) => void
  onSelect: (entry: Entry) => void
  onSeek: (at: number) => void
  onPlay: (entry: Entry) => void
}) {
  const strip = useRef<HTMLDivElement>(null)
  const [scrubbing, setScrubbing] = useState(false)
  const span = timeline.to - timeline.from
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
    <div className='space-y-2'>
      <div
        ref={strip}
        data-testid='playback-timeline'
        aria-label='录像时间轴'
        className={`relative h-12 touch-none rounded-lg border bg-muted ${
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
        onPointerMove={(event) => scrubbing && scrubTo(event.clientX)}
        onPointerUp={() => setScrubbing(false)}
      >
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
              'absolute top-1 h-10 rounded p-0',
              entry.blocked
                ? 'bg-amber-500/40'
                : entry.id === selected?.id
                  ? 'bg-primary'
                  : 'bg-emerald-600/70'
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
        {/* No playhead before the first playback: an epoch-zero position would
            clamp to the window start and read as a real position. */}
        {clock > 0 && (
          <div
            data-testid='playback-playhead'
            className='absolute top-0 bottom-0 -ml-2 w-4 cursor-grab'
            style={{ left: position(clock) }}
            onPointerDown={beginScrub}
            onPointerMove={(event) => scrubbing && scrubTo(event.clientX)}
            onPointerUp={() => setScrubbing(false)}
          >
            <div className='mx-auto h-full w-0.5 bg-red-600' />
          </div>
        )}
      </div>
      <p className='text-xs text-muted-foreground'>
        {formatClock(timeline.from, zone)} — {formatClock(timeline.to, zone)} ·{' '}
        {timeline.entries.length} 个片段 · {timeline.gaps.length}{' '}
        处缺口（单击片段选中，双击播放，拖动时间轴定位）
      </p>
    </div>
  )
}
