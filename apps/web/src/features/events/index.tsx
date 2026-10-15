import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { CheckCheck, Play, User } from 'lucide-react'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { LoadingButton } from '@/features/foundation/loading-button'
import { StatusBadge } from '@/features/foundation/status-badge'
import { CardsSkeleton } from '@/features/foundation/skeletons'
import { Empty, QueryState, SelectField } from '@/features/foundation/ui'
import { formatClock, formatMinute, wallClockInput, zoneParts } from '@/features/playback/zone'
import { cn } from '@/lib/utils'

type Event = Schema<'Event'>
type Channel = Schema<'Channel'>

const HANDLE_LABELS: Record<string, string> = {
  open: '未处理',
  acknowledged: '已确认',
  closed: '已关闭',
  false_positive: '误报',
}

const CATEGORY_LABELS: Record<string, string> = {
  person: '人',
  car: '车',
  truck: '卡车',
  bus: '巴士',
  motorcycle: '摩托车',
  bicycle: '自行车',
  dog: '狗',
  cat: '猫',
}

const SEVERITY_LABELS: Record<string, string> = {
  low: '低',
  medium: '中',
  high: '高',
}

const KIND_LABELS: Record<string, string> = {
  appear: '目标出现',
  enter: '区域进入',
  dwell: '区域停留',
}

/**
 * An MQTT `end` message can be lost. An event without an end that started
 * more than five minutes ago is closed for display purposes: showing
 * "进行中" and "抓拍生成中" on an hour-old row tells the operator a lie about
 * a dead stream. Five minutes is far beyond any real detection.
 */
const ONGOING_MAX_AGE_MS = 5 * 60_000

function isOngoing(item: Event, now: number) {
  if (item.ended_at) return false
  const start = Date.parse(item.started_at ?? '')
  return Number.isFinite(start) && now - start < ONGOING_MAX_AGE_MS
}

/** Duration pill text: seconds while short, m:ss beyond a minute. */
function durationText(item: Event, now: number) {
  if (isOngoing(item, now)) return '● 进行中'
  const end = item.ended_at ? Date.parse(item.ended_at) : NaN
  if (Number.isFinite(end)) {
    const seconds = Math.max(1, Math.round((end - Date.parse(item.started_at ?? '')) / 1000))
    return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m${pad(seconds % 60)}s`
  }
  return '已结束'
}

function pad(value: number) {
  return String(value).padStart(2, '0')
}

/** Card timestamp: today's events show only the clock, earlier ones the date too. */
function cardTimestamp(startedAt: string | undefined, zone: string, now: number) {
  const epoch = Date.parse(startedAt ?? '')
  if (!Number.isFinite(epoch)) return '—'
  const parts = zoneParts(epoch, zone)
  const today = zoneParts(now, zone)
  const sameDay =
    parts.year === today.year && parts.month === today.month && parts.day === today.day
  const clock = `${pad(parts.hour)}:${pad(parts.minute)}:${pad(parts.second)}`
  return sameDay ? clock : `${pad(parts.month)}-${pad(parts.day)} ${clock}`
}

/**
 * hitReasonText renders the facts frozen at match time. They explain the
 * decision even after the rule was later edited, which is why the reason is
 * shown instead of being recomputed from the current rule.
 */
function hitReasonText(hit: Schema<'RuleHit'>) {
  const reason = hit.reason || {}
  const parts = [
    KIND_LABELS[reason.kind ?? ''] || reason.kind,
    reason.category ? CATEGORY_LABELS[reason.category] || reason.category : '',
    typeof reason.score === 'number' ? `置信 ${Math.round(reason.score * 100)}%` : '',
    hit.zone_name ? `区域 ${hit.zone_name}` : '',
  ]
  return parts.filter(Boolean).join(' · ')
}

/**
 * M3 batch 1: the event center (EVT-01/02/03). A dense card gallery with
 * hover shortcuts, a compact one-line filter bar, and a detail drawer with
 * the handling trail. Media states are shown separately from handling state —
 * an event without a retained snapshot is not a handled event.
 */
export function Events() {
  const site = useAPI<Schema<'Site'>>('/api/v1/site')
  const zone = site.data?.timezone || ''
  const active = !!zone
  const channels = useAPI<PageData<Channel>>('/api/v1/channels?limit=100', active)
  const permitted = (channels.data?.items || []).filter((channel) =>
    channel.permissions.includes('playback')
  )
  const channelOf = new Map(
    permitted.map((channel) => [channel.id ?? '', channel])
  )
  const [channel, setChannel] = useState('')
  const [category, setCategory] = useState('')
  const [handleState, setHandleState] = useState('')
  const [selected, setSelected] = useState<string>('')
  const [openingEvent, setOpeningEvent] = useState<string>('')
  // The ticking clock drives the ongoing/closed decision; one refresh a
  // minute is plenty for a five-minute threshold.
  const [now, setNow] = useState(() => Date.now())
  // The minute tick retires stale "进行中" rows without a refetch.
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(timer)
  }, [])
  // Honest paging: the contract returns a cursor; "load more" appends pages
  // until the backend says the window is exhausted. Nothing is hidden. The
  // appended pages belong to one filter combination — changing any filter
  // resets them during render (the adjusted-state pattern, no effect needed).
  const [scope, setScope] = useState({ channel: '', category: '', handleState: '' })
  const [pages, setPages] = useState<Schema<'EventPage'>[]>([])
  if (scope.channel !== channel || scope.category !== category || scope.handleState !== handleState) {
    setScope({ channel, category, handleState })
    setPages([])
  }
  // The 7-day window is anchored once per mount: re-anchoring it on every
  // render would refetch the list continuously.
  const [window] = useState(() => {
    const end = Date.now()
    return { start: end - 7 * 86_400_000, end }
  })
  // The page opens on every channel, not on an arbitrary first one: an
  // operator asking "are there events" must not have to guess which channel
  // the page happened to select. Choosing a channel in the filter narrows it.
  const filters = (() => {
    const params = new URLSearchParams({
      start: String(window.start),
      end: String(window.end),
      limit: '50',
    })
    if (channel) params.set('channel_id', channel)
    if (category) params.set('category', category)
    if (handleState) params.set('handle_state', handleState)
    return params.toString()
  })()
  const events = useAPI<Schema<'EventPage'>>(
    `/api/v1/events?${filters}`,
    active
  )
  const morePath =
    events.data?.next_cursor
      ? `/api/v1/events?${new URLSearchParams({
          start: String(window.start),
          end: String(window.end),
          limit: '50',
          cursor: events.data.next_cursor,
          ...(channel ? { channel_id: channel } : {}),
          ...(category ? { category } : {}),
          ...(handleState ? { handle_state: handleState } : {}),
        }).toString()}`
      : ''
  const more = useAPI<Schema<'EventPage'>>(morePath, active && !!morePath && pages.length > 0)
  const loadedPages = [events.data, ...pages]
  const items = loadedPages.flatMap((page) => page?.items || [])
  const lastCursor =
    pages.length && more.data ? more.data.next_cursor : events.data?.next_cursor
  const hasMore = !!lastCursor
  const loadMore = () => {
    if (!more.data) return
    setPages((current) => [...current, more.data!])
  }
  const client = useQueryClient()
  const [markAllPending, setMarkAllPending] = useState(false)
  const [markAllError, setMarkAllError] = useState('')
  const openCount = items.filter((item) => item.handle_state === 'open').length

  // One request per open event, sequentially: a mass transition is a rare
  // cleanup, and each row's version travels with its own PATCH.
  async function markAllHandled() {
    setMarkAllPending(true)
    setMarkAllError('')
    try {
      for (const item of items.filter((entry) => entry.handle_state === 'open')) {
        await apiRequest(
          `/api/v1/events/${item.id}`,
          jsonRequest('PATCH', {
            state: 'acknowledged',
            note: '',
            version: item.version ?? 0,
          })
        )
      }
      await client.invalidateQueries()
    } catch (cause) {
      setMarkAllError(apiErrorText(cause))
    } finally {
      setMarkAllPending(false)
    }
  }

  async function markOneHandled(item: Event) {
    await apiRequest(
      `/api/v1/events/${item.id}`,
      jsonRequest('PATCH', {
        state: 'acknowledged',
        note: '',
        version: item.version ?? 0,
      })
    )
    await client.invalidateQueries()
  }

  if (!permitted.length && channels.data)
    return <Empty>没有可查看事件的通道，请联系管理员分配录像回放权限。</Empty>

  return (
    <div className='space-y-4'>
      <div>
        <h1 className='text-2xl font-semibold tracking-tight'>事件</h1>
        <p className='mt-1 text-sm text-muted-foreground'>
          智能检测事件（最近 7 天），点击卡片查看抓拍与处理详情。
          {zone ? ` 时间按站点时区 ${zone} 显示。` : ''}
        </p>
      </div>
      <QueryState
        pending={channels.isPending || site.isPending}
        error={channels.error || site.error}
        retry={() => void channels.refetch()}
      />
      {!!zone && !!permitted.length && (
        <>
          <div className='flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-card px-3.5 py-2.5'>
            <div className='flex flex-wrap items-center gap-x-4 gap-y-2'>
              <div className='flex items-center gap-2'>
                <span className='text-xs text-muted-foreground'>通道:</span>
                <SelectField
                  ariaLabel='通道筛选'
                  value={channel}
                  onValueChange={(next) => setChannel(next ?? '')}
                  options={[
                    { value: '', label: '全部通道' },
                    ...permitted.map((channel) => ({
                      value: channel.id ?? '',
                      label: `CH${String(channel.channel_no ?? 0).padStart(2, '0')} ${channel.channel_name}`,
                    })),
                  ]}
                  className='w-40'
                />
              </div>
              <div className='flex items-center gap-2'>
                <span className='text-xs text-muted-foreground'>类别:</span>
                <SelectField
                  ariaLabel='类别筛选'
                  value={category}
                  onValueChange={(next) => setCategory(next ?? '')}
                  options={[
                    { value: '', label: '全部' },
                    ...Object.entries(CATEGORY_LABELS).map(([value, label]) => ({ value, label })),
                  ]}
                  className='w-24'
                />
              </div>
              <div
                role='group'
                aria-label='处理状态筛选'
                className='flex items-center gap-1'
              >
                {(
                  [
                    { value: '', label: '全部' },
                    { value: 'open', label: '未处理' },
                    { value: 'acknowledged', label: '已处理' },
                  ] as const
                ).map(({ value, label }) => {
                  const isActive = handleState === value
                  return (
                    <button
                      key={value || 'all'}
                      type='button'
                      aria-pressed={isActive}
                      onClick={() => setHandleState(value)}
                      className={cn(
                        'inline-flex h-7 items-center rounded-full border px-2.5 text-xs transition-colors',
                        isActive
                          ? 'border-primary bg-primary/10 font-medium text-primary'
                          : 'border-border text-muted-foreground hover:bg-accent hover:text-foreground'
                      )}
                    >
                      {label}
                    </button>
                  )
                })}
              </div>
            </div>
            <div className='flex flex-wrap items-center gap-3'>
              {markAllError && (
                <span role='alert' className='text-xs text-destructive'>
                  {markAllError}
                </span>
              )}
              <span className='text-xs tabular-nums text-muted-foreground'>
                已加载 {items.length} 条
              </span>
              <Button
                type='button'
                size='sm'
                variant='outline'
                disabled={markAllPending || openCount === 0}
                onClick={() => void markAllHandled()}
              >
                <CheckCheck aria-hidden className='size-3.5' />
                {markAllPending ? '标记中…' : `全部标为已处理${openCount ? ` (${openCount})` : ''}`}
              </Button>
            </div>
          </div>
          <QueryState
            pending={events.isPending}
            error={events.error || more.error}
            retry={() => {
              void events.refetch()
              if (more.error) void more.refetch()
            }}
            skeleton={<CardsSkeleton />}
          />
          {events.data && !items.length && <Empty>该筛选范围内没有事件。</Empty>}
          <div className='grid grid-cols-1 gap-3.5 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4'>
            {items.map((item) => {
              const channelItem = channelOf.get(item.channel_id ?? '')
              const channelLabel = channelItem
                ? `CH${String(channelItem.channel_no ?? 0).padStart(2, '0')} ${channelItem.channel_name}`
                : '未知通道'
              const ongoing = isOngoing(item, now)
              const handleState = item.handle_state ?? 'open'
              const scorePct = Math.round((item.score ?? 0) * 100)
              return (
                <div
                  key={item.id}
                  data-testid='event-card'
                  role='button'
                  tabIndex={0}
                  className='group cursor-pointer overflow-hidden rounded-xl border bg-card text-left text-sm transition-colors hover:border-zinc-600 hover:shadow-md'
                  onClick={() => setSelected(item.id ?? '')}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter' || event.key === ' ') {
                      event.preventDefault()
                      setSelected(item.id ?? '')
                    }
                  }}
                >
                  <div className='relative aspect-[4/3] overflow-hidden rounded-t-xl bg-zinc-950'>
                    {item.snapshot_state === 'ready' ? (
                      <img
                        src={`/api/v1/events/${item.id}/snapshot`}
                        alt='事件抓拍'
                        loading='lazy'
                        onError={() => setOpeningEvent(item.id ?? '')}
                        className='h-full w-full object-cover'
                      />
                    ) : (
                      // Pending only reads as live within the ongoing window;
                      // an old snapshotless event shows the honest dead state.
                      <div className='grid h-full w-full place-items-center text-zinc-600'>
                        {item.snapshot_state === 'pending' && ongoing ? (
                          <span className='flex flex-col items-center gap-1.5 text-xs'>
                            <span
                              aria-hidden
                              className='size-2 animate-pulse rounded-full bg-amber-400'
                            />
                            抓拍生成中
                          </span>
                        ) : (
                          <span className='flex flex-col items-center gap-1.5 text-xs'>
                            <User aria-hidden className='size-6' />
                            无抓拍图像
                          </span>
                        )}
                      </div>
                    )}
                    {/* Corner overlays: category score top-left, handling top-right. */}
                    <span
                      className={cn(
                        'absolute left-2 top-2 inline-flex items-center gap-1 rounded-full border border-white/10 bg-black/50 px-2 py-0.5 text-xs backdrop-blur',
                        scorePct >= 90 ? 'text-emerald-300' : 'text-zinc-200'
                      )}
                    >
                      <User aria-hidden className='size-3' />
                      {CATEGORY_LABELS[item.category ?? ''] || item.category} {scorePct}%
                    </span>
                    <span
                      className={cn(
                        'absolute right-2 top-2 inline-flex items-center rounded-full border px-2 py-0.5 text-xs backdrop-blur',
                        handleState === 'open'
                          ? 'border-amber-500/40 bg-amber-950/60 text-amber-300'
                          : 'border-white/10 bg-black/50 text-zinc-300'
                      )}
                    >
                      {HANDLE_LABELS[handleState]}
                    </span>
                    {/* Hover shortcuts: jump straight into playback or mark the
                        event handled without opening the drawer. */}
                    <div
                      className='absolute bottom-2 right-2 flex gap-1.5 opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100'
                      onClick={(e) => e.stopPropagation()}
                      onKeyDown={(e) => e.stopPropagation()}
                    >
                      <Link
                        to='/recordings'
                        search={{
                          channel: item.channel_id ?? '',
                          at: (() => {
                            const at = new Date(Date.parse(item.started_at ?? '')).getTime() - 10_000
                            return wallClockInput(at, zone)
                          })(),
                          tab: 'playback',
                        }}
                        aria-label={`查看事件录像 ${cardTimestamp(item.started_at, zone, now)}`}
                        title='录像回放'
                        className='grid size-7 place-items-center rounded-md border border-white/10 bg-black/60 text-zinc-200 backdrop-blur hover:bg-black/80'
                      >
                        <Play aria-hidden className='size-3.5' />
                      </Link>
                      {handleState === 'open' && (
                        <button
                          type='button'
                          aria-label='标为已处理'
                          title='标为已处理'
                          className='grid size-7 place-items-center rounded-md border border-emerald-500/30 bg-emerald-950/70 text-emerald-300 backdrop-blur hover:bg-emerald-900/80'
                          disabled={markAllPending}
                          onClick={(e) => {
                            e.stopPropagation()
                            void markOneHandled(item).catch(() => {})
                          }}
                        >
                          ✓
                        </button>
                      )}
                    </div>
                  </div>
                  <div className='flex items-center justify-between gap-2 px-3 py-2.5 text-xs'>
                    <div className='flex min-w-0 items-center gap-1.5'>
                      <span className='shrink-0 rounded-md bg-muted px-1.5 py-0.5 font-medium text-foreground'>
                        {channelLabel}
                      </span>
                      {item.zones?.length ? (
                        <span className='truncate rounded-md bg-muted px-1.5 py-0.5 text-muted-foreground'>
                          {item.zones.join('/')}
                        </span>
                      ) : null}
                    </div>
                    <div className='flex shrink-0 items-center gap-2 text-muted-foreground'>
                      <span title={formatClock(Date.parse(item.started_at ?? ''), zone)}>
                        {cardTimestamp(item.started_at, zone, now)}
                      </span>
                      <span
                        className={cn(
                          'inline-flex items-center rounded-full border px-1.5 py-px tabular-nums',
                          ongoing
                            ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-400'
                            : 'border-border text-muted-foreground'
                        )}
                      >
                        {durationText(item, now)}
                      </span>
                    </div>
                  </div>
                </div>
              )
            })}
          </div>
          {more.isPending && pages.length > 0 && (
            <p role='status' className='text-sm text-muted-foreground'>
              正在加载更多事件…
            </p>
          )}
          {/* The window is a hard 7 days; the cursor says whether more exists —
              both facts are stated so a truncated list never reads as "all". */}
          {items.length > 0 && (
            <div className='flex items-center justify-between text-xs text-muted-foreground'>
              <span>已显示 {items.length} 个事件{hasMore ? '，还有更早的' : '，本窗口已全部加载'}</span>
              {hasMore && (
                <Button
                  size='sm'
                  variant='outline'
                  disabled={more.isPending}
                  onClick={loadMore}
                >
                  加载更早事件
                </Button>
              )}
            </div>
          )}
          {selected && (
            <EventDetail
              eventID={selected}
              zone={zone}
              onClose={() => setSelected('')}
              snapshotFailed={openingEvent === selected}
            />
          )}
        </>
      )}
    </div>
  )
}

function EventDetail({
  eventID,
  zone,
  onClose,
  snapshotFailed,
}: {
  eventID: string
  zone: string
  onClose: () => void
  snapshotFailed: boolean
}) {
  const client = useQueryClient()
  const event = useAPI<Event>(`/api/v1/events/${eventID}`)
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  const [pendingAction, setPendingAction] = useState('')
  const [feedbackPending, setFeedbackPending] = useState('')
  const item = event.data
  const [now] = useState(() => Date.now())
  const ongoing = isOngoing(item ?? {}, now)

  // Rule-hit feedback is operational: it records the operator's judgement and
  // keeps the original reason. It never edits the rule or retrains anything.
  async function markHit(hitID: string, falsePositive: boolean, hitNote: string) {
    setFeedbackPending(hitID)
    setError('')
    try {
      await apiRequest(
        `/api/v1/event-hits/${hitID}`,
        jsonRequest('PATCH', { false_positive: falsePositive, note: hitNote })
      )
      await client.invalidateQueries()
    } catch (cause) {
      setError(apiErrorText(cause))
    } finally {
      setFeedbackPending('')
    }
  }

  async function transition(state: string) {
    if (!item) return
    setPending(true)
    setPendingAction(state)
    setError('')
    try {
      const init = jsonRequest('PATCH', { state, note, version: item.version ?? 0 })
      await apiRequest(`/api/v1/events/${eventID}`, init)
      setNote('')
      await client.invalidateQueries()
    } catch (cause) {
      setError(apiErrorText(cause))
    } finally {
      setPending(false)
      setPendingAction('')
    }
  }

  return (
    <Sheet open onOpenChange={(next) => !next && onClose()}>
      <SheetContent className='flex flex-col gap-4 overflow-y-auto sm:max-w-lg'>
        <SheetHeader>
          <SheetTitle>事件详情</SheetTitle>
          <SheetDescription>
            {item
              ? `${CATEGORY_LABELS[item.category ?? ''] || item.category} · ${HANDLE_LABELS[item.handle_state ?? 'open']}`
              : '正在读取…'}
          </SheetDescription>
        </SheetHeader>
        <QueryState pending={event.isPending} error={event.error} retry={event.refetch} />
        {item && (
          <div className='space-y-3 text-sm'>
            {item.snapshot_state === 'ready' && !snapshotFailed ? (
              <img
                src={`/api/v1/events/${item.id}/snapshot`}
                alt='事件抓拍'
                className='w-full rounded-lg'
              />
            ) : (
              <div className='grid aspect-video w-full place-items-center rounded-lg bg-muted text-xs text-muted-foreground'>
                {item.snapshot_state === 'ready'
                  ? '抓拍读取失败'
                  : item.snapshot_state === 'pending' && ongoing
                    ? '抓拍生成中'
                    : '无抓拍图像'}
              </div>
            )}
            <div className='grid grid-cols-2 gap-2 rounded-lg border bg-muted/40 p-3'>
              <span className='text-muted-foreground'>开始</span>
              <span>{formatClock(Date.parse(item.started_at ?? ''), zone)}</span>
              <span className='text-muted-foreground'>结束</span>
              <span>
                {item.ended_at
                  ? formatClock(Date.parse(item.ended_at), zone)
                  : ongoing
                    ? '进行中'
                    : '已结束（结束消息未收到）'}
              </span>
              <span className='text-muted-foreground'>分数</span>
              <span>{Math.round((item.score ?? 0) * 100)}%</span>
              <span className='text-muted-foreground'>区域</span>
              <span>{item.zones?.length ? item.zones.join('、') : '—'}</span>
              <span className='text-muted-foreground'>索引保留至</span>
              <span>{item.expires_at ? formatMinute(Date.parse(item.expires_at), zone) : '—'}</span>
            </div>
            <div className='flex flex-wrap items-center gap-2'>
              <Button asChild size='sm' variant='outline'>
                <Link
                  to='/recordings'
                  search={{
                    channel: item.channel_id ?? '',
                    at: (() => {
                      const at = new Date(Date.parse(item.started_at ?? '')).getTime() - 10_000
                      return wallClockInput(at, zone)
                    })(),
                    tab: 'playback',
                  }}
                >
                  查看事件录像
                </Link>
              </Button>
            </div>
            <div className='space-y-2 rounded-lg border p-3'>
              <p className='font-medium'>命中规则</p>
              {!item.hits?.length && (
                <p className='text-xs text-muted-foreground'>
                  没有规则命中这个事件。
                </p>
              )}
              {(item.hits || []).map((hit) => (
                <div
                  key={hit.id}
                  data-testid='event-hit'
                  className='space-y-1 rounded-md border bg-muted/30 p-2'
                >
                  <div className='flex flex-wrap items-center gap-2'>
                    <span className='font-medium'>{hit.rule_name}</span>
                    <StatusBadge
                      tone={
                        hit.severity === 'high'
                          ? 'destructive'
                          : hit.severity === 'medium'
                            ? 'info'
                            : 'neutral'
                      }
                    >
                      {SEVERITY_LABELS[hit.severity ?? ''] || hit.severity}
                    </StatusBadge>
                    {hit.false_positive && <StatusBadge tone='warning'>已标误报</StatusBadge>}
                  </div>
                  {/* The reason is frozen at match time, so it explains the
                      decision even after the rule was edited. */}
                  <p className='text-xs text-muted-foreground'>
                    {hitReasonText(hit)}
                  </p>
                  {hit.false_positive_note && (
                    <p className='text-xs text-muted-foreground'>备注：{hit.false_positive_note}</p>
                  )}
                  <div className='flex items-center gap-2'>
                    <Button
                      size='sm'
                      variant='ghost'
                      aria-label={`标记规则命中${hit.false_positive ? '非误报' : '为误报'} ${hit.rule_name}`}
                      disabled={feedbackPending === hit.id}
                      onClick={() =>
                        void markHit(
                          hit.id ?? '',
                          !hit.false_positive,
                          hit.false_positive ? '' : '误报'
                        )
                      }
                    >
                      {hit.false_positive ? '取消误报' : '标记误报'}
                    </Button>
                  </div>
                </div>
              ))}
            </div>
            <div className='space-y-2 rounded-lg border p-3'>
              <p className='font-medium'>处理</p>
              {error && (
                <p role='alert' className='text-sm text-destructive'>
                  {error}
                </p>
              )}
              <Input
                aria-label='处理备注'
                placeholder='备注（可选）'
                value={note}
                maxLength={500}
                onChange={(input) => setNote(input.target.value)}
              />
              {/* Each action owns its pending state, so the operator sees which
                  transition is running; the rest stay clickable-disabled. */}
              <div className='flex flex-wrap gap-2'>
                <LoadingButton
                  size='sm'
                  pending={pendingAction === 'acknowledged'}
                  disabled={pending && pendingAction !== 'acknowledged'}
                  pendingText='确认中…'
                  onClick={() => void transition('acknowledged')}
                >
                  已确认
                </LoadingButton>
                <LoadingButton
                  size='sm'
                  variant='outline'
                  pending={pendingAction === 'closed'}
                  disabled={pending && pendingAction !== 'closed'}
                  pendingText='关闭中…'
                  onClick={() => void transition('closed')}
                >
                  已关闭
                </LoadingButton>
                <LoadingButton
                  size='sm'
                  variant='outline'
                  pending={pendingAction === 'false_positive'}
                  disabled={pending && pendingAction !== 'false_positive'}
                  pendingText='标记中…'
                  onClick={() => void transition('false_positive')}
                >
                  误报
                </LoadingButton>
                <LoadingButton
                  size='sm'
                  variant='ghost'
                  pending={pendingAction === 'open'}
                  disabled={pending && pendingAction !== 'open'}
                  pendingText='打开中…'
                  onClick={() => void transition('open')}
                >
                  重新打开
                </LoadingButton>
              </div>
            </div>
            <div className='space-y-2'>
              <p className='font-medium'>处理记录</p>
              {!item.handling?.length && (
                <p className='text-xs text-muted-foreground'>暂无处理记录。</p>
              )}
              {/* The trail reads as a timeline: one rail, one node per action. */}
              {item.handling && item.handling.length > 0 && (
                <ol className='relative space-y-3 border-l pl-4'>
                  {(item.handling || []).map((record) => (
                    <li key={record.id} className='relative'>
                      <span
                        aria-hidden
                        className='absolute -left-[21px] top-1.5 size-2 rounded-full bg-muted-foreground/50'
                      />
                      <p className='text-xs'>
                        {HANDLE_LABELS[record.state ?? ''] || record.state}
                        <span className='ml-2 text-muted-foreground'>
                          {record.actor_name} ·{' '}
                          {formatClock(Date.parse(record.created_at ?? ''), zone)}
                        </span>
                      </p>
                      {record.note && (
                        <p className='mt-0.5 text-xs text-muted-foreground'>
                          {record.note}
                        </p>
                      )}
                    </li>
                  ))}
                </ol>
              )}
            </div>
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}
