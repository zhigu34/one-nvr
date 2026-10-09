import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
} from 'react'
import { Link } from '@tanstack/react-router'
import {
  Maximize,
  Pause,
  Play,
  RotateCcw,
  SkipForward,
  Square,
} from 'lucide-react'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
import { useAPI } from '@/features/foundation/hooks'
import { Empty, QueryState, SelectField } from '@/features/foundation/ui'
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

const recordingsPath = (
  channelID: string,
  range: { start: number; end: number }
) =>
  `/api/v1/recordings?${new URLSearchParams({
    channel_id: channelID,
    start: new Date(range.start).toISOString(),
    end: new Date(range.end).toISOString(),
    limit: '100',
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
}: {
  initialChannel?: string
  initialAt?: string
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
      <div>
        <h1 className='text-2xl font-semibold tracking-tight'>录像回放</h1>
        <p className='mt-1 text-sm text-muted-foreground'>
          {zone
            ? `按站点时区 ${zone} 检索与播放。进入本页只加载索引，播放由播放按钮、双击片段或时间轴定位触发。`
            : '正在读取站点时区…'}
        </p>
      </div>
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
  const [search, setSearch] = useState('')
  const query = useAPI<Schema<'RecordingPage'>>(
    recordingsPath(channelID, range)
  )
  const timeline = useMemo(
    () => buildTimeline(query.data?.items || [], range.start, range.end),
    [query.data, range]
  )
  const video = useRef<HTMLVideoElement>(null)
  const frame = useRef<HTMLDivElement>(null)
  const controller = useRef<PlaybackController | null>(null)
  const [state, setState] = useState<PlaybackState>(IDLE)
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
          <Button variant='outline' onClick={() => controller.current?.stop()}>
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
            当前位置 {formatClock(state.clock, zone)}
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
          pending={query.isPending}
          error={query.error}
          retry={query.refetch}
        />
        <Timeline
          timeline={timeline}
          zone={zone}
          clock={state.clock}
          selected={selected}
          onSelect={setSelected}
          onSeek={(at) => controller.current?.seek(at)}
          onPlay={(entry) => {
            setSelected(entry)
            controller.current?.start(entry.start)
          }}
        />
        {query.data && !timeline.entries.length && (
          <Empty>该时间范围没有已登记录像</Empty>
        )}
        {selected && (
          <div className='rounded-xl border bg-card p-3 text-sm'>
            <p className='font-medium'>
              片段 {selected.id.slice(0, 8)} · {selected.blocked || '可用'}
            </p>
            <p className='mt-1 text-muted-foreground'>
              {formatClock(selected.start, zone)} —{' '}
              {formatClock(selected.end, zone)} ·{' '}
              {formatDuration(selected.end - selected.start)} ·{' '}
              {(selected.bytes / 1048576).toFixed(1)} MiB
            </p>
          </div>
        )}
        <p className='text-xs text-muted-foreground'>
          正在回放 {label(active)}。暂停、拖动、±10
          秒与倍速都在同一条时间轴上生效。
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
  onSelect,
  onSeek,
  onPlay,
}: {
  timeline: Timeline
  zone: string
  clock: number
  selected: Entry | null
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
  if (!timeline.entries.length || span <= 0)
    return (
      <p className='rounded-xl border border-dashed p-4 text-center text-sm text-muted-foreground'>
        时间轴上没有可绘制的片段
      </p>
    )
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
          // Only the empty background positions playback; a bar click selects.
          if (event.target === event.currentTarget) beginScrub(event)
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
      </div>
      <p className='text-xs text-muted-foreground'>
        {formatClock(timeline.from, zone)} — {formatClock(timeline.to, zone)} ·{' '}
        {timeline.entries.length} 个片段 · {timeline.gaps.length}{' '}
        处缺口（单击片段选中，双击播放，拖动时间轴定位）
      </p>
    </div>
  )
}
