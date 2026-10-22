import { useEffect, useState } from 'react'
import { configuredChannels } from '@/features/foundation/channels'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { AlertTriangle, ImageOff, User } from 'lucide-react'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { StatusBadge } from '@/features/foundation/status-badge'
import { CardsSkeleton } from '@/features/foundation/skeletons'
import { Empty, QueryState, SelectField } from '@/features/foundation/ui'
import { formatClock, formatMinute, wallClockInput, zoneParts } from '@/features/playback/zone'
import { cn } from '@/lib/utils'
import { eventSeconds, missingSnapshotReason } from './snapshot-state'

type Event = Schema<'Event'>
type Channel = Schema<'Channel'>

// EVT-05's five states in the operator's words. Kept beside the handling labels
// so the two vocabularies stay visibly distinct.
const DELIVERY_LABELS: Record<string, string> = {
  sending: '发送中',
  failed: '失败',
  retrying: '已重试',
  succeeded: '成功',
  exhausted: '最终失败',
}

const DELIVERY_TONES: Record<string, string> = {
  succeeded: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
  sending: 'border-border text-muted-foreground',
  retrying: 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400',
  failed: 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400',
  exhausted: 'border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400',
}

/**
 * The badge answers "can I watch this", because that is the one thing the card
 * is for: an event is evidence, not a ticket. Handling states were removed from
 * the product; what remains is whether footage exists, and the time is the way
 * into it.
 */
const MEDIA_BADGES: Record<string, { label: string; tone: string }> = {
  linked: {
    label: '已录像',
    tone: 'border-emerald-500/40 bg-emerald-950/60 text-emerald-300',
  },
  partial: {
    label: '部分录像',
    tone: 'border-amber-500/40 bg-amber-950/60 text-amber-300',
  },
  unavailable: {
    label: '未录像',
    tone: 'border-white/10 bg-black/50 text-zinc-300',
  },
  pending: {
    label: '录像生成中',
    tone: 'border-white/10 bg-black/50 text-zinc-300',
  },
}

/**
 * The badge for one event. A running event's clip is still being extended — the
 * segment is not published yet — so claiming 已录像 would promise a file the
 * player cannot open. That is the same reason the timestamp is not a link while
 * the event runs.
 */
function mediaBadge(event: Schema<'Event'>, running: boolean) {
  if (running && (event.media_state === 'linked' || event.media_state === 'partial')) {
    return MEDIA_BADGES.pending
  }
  return MEDIA_BADGES[event.media_state ?? 'pending'] ?? MEDIA_BADGES.pending
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
 * An MQTT `end` message can be lost. An event without an end that started more
 * than five minutes ago is closed for display purposes: showing "进行中" and
 * "抓拍生成中" on an hour-old row would tell the operator a lie about a dead
 * stream. Five minutes is far beyond any real detection.
 *
 * The worker reconciles those events against the detector's API a few minutes
 * later, so a lost message is repaired rather than displayed forever — but the
 * page must stay honest in the window before that happens.
 */
const ONGOING_MAX_AGE_MS = 5 * 60_000

function isOngoing(item: Event, now: number) {
  if (item.ended_at) return false
  const start = Date.parse(item.started_at ?? '')
  return Number.isFinite(start) && now - start < ONGOING_MAX_AGE_MS
}

/**
 * An event the upstream never closed. Distinct from "ended long ago": here the
 * system knows the detection started and does not know how it finished, which
 * is why its snapshot may never arrive.
 */
function endedWithoutUpstreamClose(item: Event, now: number) {
  return !item.ended_at && !isOngoing(item, now)
}

/** Duration pill text: seconds while short, m:ss beyond a minute. */
function durationText(item: Event, now: number) {
  if (isOngoing(item, now)) return '● 进行中'
  const end = item.ended_at ? Date.parse(item.ended_at) : NaN
  if (Number.isFinite(end)) {
    const seconds = Math.max(1, Math.round((end - Date.parse(item.started_at ?? '')) / 1000))
    return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m${pad(seconds % 60)}s`
  }
  if (endedWithoutUpstreamClose(item, now)) return '结束未知'
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
  const permitted = configuredChannels(
    (channels.data?.items || []).filter((channel) => channel.permissions.includes('playback'))
  )
  const channelOf = new Map(
    permitted.map((channel) => [channel.id ?? '', channel])
  )
  const [channel, setChannel] = useState('')
  const [category, setCategory] = useState('')
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
  const [scope, setScope] = useState({ channel: '', category: '' })
  const [pages, setPages] = useState<Schema<'EventPage'>[]>([])
  if (scope.channel !== channel || scope.category !== category) {
    setScope({ channel, category })
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
            </div>
            <div className='flex flex-wrap items-center gap-3'>
              {/* "已加载" beside a "全部…" button read as "these are all of
                  them". The count is the first page, and saying so is what
                  stops an operator from believing the index holds 50 events. */}
              <span
                className='text-xs tabular-nums text-muted-foreground'
                title='列表按时间倒序分页加载，这里只统计已取到的部分'
              >
                已取 {items.length} 条
                {hasMore ? '（还有更早的）' : ''}
              </span>
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
              const scorePct = Math.round((item.score ?? 0) * 100)
              const badge = mediaBadge(item, ongoing)
              // Where the timestamp would jump to. Null means "not a link":
              // no footage, still being produced, or no playback grant. Seek to
              // the available start rather than the event's own start — the
              // segment boundary lands wherever the keyframe put it.
              const hasFootage =
                (item.media_state === 'linked' || item.media_state === 'partial') && !!item.ended_at
              const playbackAt = hasFootage
                ? item.media_start_at
                  ? Date.parse(item.media_start_at)
                  : Date.parse(item.started_at ?? '')
                : null
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
                      // Three states, three different truths. "No picture" was
                      // one message for two causes: the detector had nothing to
                      // save, or the event never closed so nobody knows. The
                      // second is a fault and says so; the first is just a
                      // detection with no snapshot. Both are drawn light — a
                      // wall of full-bleed black tiles buries the cards that do
                      // have a picture.
                      <div className='grid h-full w-full place-items-center bg-zinc-900/40 text-zinc-500'>
                        {item.snapshot_state === 'pending' && ongoing ? (
                          <span className='flex flex-col items-center gap-1.5 text-xs'>
                            <span
                              aria-hidden
                              className='size-2 animate-pulse rounded-full bg-amber-400'
                            />
                            抓拍生成中
                          </span>
                        ) : endedWithoutUpstreamClose(item, now) ? (
                          <span
                            className='flex flex-col items-center gap-1.5 px-2 text-center text-xs'
                            title='检测器没有送出这条事件的结束消息，抓拍可能因此一直没有到达。系统会向检测器核对并补齐。'
                          >
                            <AlertTriangle aria-hidden className='size-4 text-amber-500' />
                            抓拍未到达
                            <span className='text-[10px] opacity-80'>事件未正常结束</span>
                          </span>
                        ) : item.snapshot_state === 'pending' ? (
                          // The event closed but the picture has not arrived yet.
                          // The detector announces snapshots as they stand when an
                          // event ends, so one written just after is never
                          // announced; reconciliation asks about it directly.
                          // Until then this is neither "loading" nor "missing".
                          <span
                            className='flex flex-col items-center gap-1.5 px-2 text-center text-xs'
                            title='这条事件已结束，但检测器当时还没有生成抓拍。系统会向检测器核对，确认有就补回、确认没有就标记为无图。'
                          >
                            <span
                              aria-hidden
                              className='size-2 animate-pulse rounded-full bg-zinc-500'
                            />
                            抓拍待同步
                            <span className='text-[10px] opacity-80'>稍后自动核对</span>
                          </span>
                        ) : missingSnapshotReason(item) === 'too_short' ? (
                          // The detector never had this picture: it writes
                          // snapshots while an event runs, and a one-second
                          // detection can end before the first one lands. Saying
                          // "无抓拍图像" here would read as a fault and send the
                          // operator looking for something that does not exist.
                          <span
                            className='flex flex-col items-center gap-1.5 px-2 text-center text-xs'
                            title='事件持续时间短于检测器写入抓拍的最短间隔，因此从未生成这张图；不是抓拍丢失。'
                          >
                            <ImageOff aria-hidden className='size-4' />
                            事件过短，未及抓拍
                            <span className='text-[10px] opacity-80'>
                              持续 {eventSeconds(item) ?? 0} 秒
                            </span>
                          </span>
                        ) : (
                          <span className='flex flex-col items-center gap-1.5 text-xs'>
                            <ImageOff aria-hidden className='size-4' />
                            无抓拍图像
                          </span>
                        )}
                      </div>
                    )}
                    {/* Corner overlays: category score top-left, recording state
                        top-right. The badge answers the one question the card is
                        for — is there something to watch. */}
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
                        badge.tone
                      )}
                    >
                      {badge.label}
                    </span>
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
                      {/* The time is the way into the footage. Clicking the card
                          still opens the drawer (the picture is what an operator
                          points at); the timestamp is the thing that means "this
                          moment", so that is what jumps to the recording. Only
                          when footage exists: a link into an empty range reads
                          as a broken recording. */}
                      {playbackAt != null ? (
                        <Link
                          to='/recordings'
                          search={{
                            channel: item.channel_id ?? '',
                            at: wallClockInput(playbackAt, zone),
                            tab: 'playback',
                          }}
                          aria-label={`查看事件录像 ${formatClock(Date.parse(item.started_at ?? ''), zone)}`}
                          title={`跳到录像 ${formatClock(playbackAt, zone)}`}
                          className='rounded px-0.5 tabular-nums underline decoration-dotted underline-offset-2 transition-colors hover:text-foreground'
                          onClick={(event) => event.stopPropagation()}
                          onKeyDown={(event) => event.stopPropagation()}
                        >
                          {cardTimestamp(item.started_at, zone, now)}
                        </Link>
                      ) : (
                        <span
                          className='tabular-nums'
                          title={formatClock(Date.parse(item.started_at ?? ''), zone)}
                        >
                          {cardTimestamp(item.started_at, zone, now)}
                        </span>
                      )}
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
  // What happened to the alert for this event. Fetched with the detail rather
  // than the list: only the drawer shows it.
  const deliveries = useAPI<Schema<'NotificationDeliveryPage'>>(
    `/api/v1/events/${eventID}/deliveries`,
    !!eventID
  )
  const [error, setError] = useState('')
  const [feedbackPending, setFeedbackPending] = useState('')
  const item = event.data
  const deliveryItems = deliveries.data?.items ?? []
  const [now] = useState(() => Date.now())
  const ongoing = isOngoing(item ?? {}, now)
  // Same rule as the card, so the drawer and the tile cannot disagree about
  // whether footage exists.
  const hasFootage =
    !!item && (item.media_state === 'linked' || item.media_state === 'partial') && !!item.ended_at
  const playbackAt = hasFootage
    ? item?.media_start_at
      ? Date.parse(item.media_start_at)
      : Date.parse(item?.started_at ?? '')
    : null

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

  return (
    <Sheet open onOpenChange={(next) => !next && onClose()}>
      <SheetContent className='flex flex-col gap-4 overflow-y-auto sm:max-w-lg'>
        <SheetHeader>
          <SheetTitle>事件详情</SheetTitle>
          <SheetDescription>
            {item
              ? `${CATEGORY_LABELS[item.category ?? ''] || item.category} · ${mediaBadge(item, ongoing).label}`
              : '正在读取…'}
          </SheetDescription>
        </SheetHeader>
        <QueryState pending={event.isPending} error={event.error} retry={event.refetch} />
        {error && (
          <p role='alert' className='text-sm text-destructive'>
            {error}
          </p>
        )}
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
                    : missingSnapshotReason(item) === 'too_short'
                      ? `事件过短（持续 ${eventSeconds(item) ?? 0} 秒），检测器未及写入抓拍`
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
                    : '未收到结束消息（系统会向检测器核对）'}
              </span>
              <span className='text-muted-foreground'>分数</span>
              <span>{Math.round((item.score ?? 0) * 100)}%</span>
              <span className='text-muted-foreground'>区域</span>
              <span>{item.zones?.length ? item.zones.join('、') : '—'}</span>
              <span className='text-muted-foreground'>索引保留至</span>
              <span>{item.expires_at ? formatMinute(Date.parse(item.expires_at), zone) : '—'}</span>
            </div>
            <div className='flex flex-wrap items-center gap-2'>
              {/* The same rule as the card: a link means footage exists. The
                  badge in the header already says 未录像, so an absent link
                  needs no second explanation here. */}
              {hasFootage && (
                <Button asChild size='sm' variant='outline'>
                  <Link
                    to='/recordings'
                    search={{
                      channel: item.channel_id ?? '',
                      at: wallClockInput(playbackAt ?? Date.parse(item.started_at ?? ''), zone),
                      tab: 'playback',
                    }}
                  >
                    查看事件录像
                  </Link>
                </Button>
              )}
              {!hasFootage && (
                <span className='text-xs text-muted-foreground'>
                  本机无录像 · {formatClock(Date.parse(item.started_at ?? ''), zone)}
                </span>
              )}
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
              <p className='font-medium'>通知</p>
              {deliveries.isPending && <p className='text-xs text-muted-foreground'>读取中…</p>}
              {!deliveries.isPending && !deliveryItems.length && (
                <p className='text-xs text-muted-foreground'>
                  这个事件没有触发通知：可能是规则未绑定渠道，或该规则未命中。
                </p>
              )}
              {deliveryItems.map((item) => (
                <div key={`${item.notification_id}-${item.channel_id}`} className='flex flex-wrap items-center gap-2 text-xs'>
                  <span className='font-medium'>{item.channel_name}</span>
                  <span className={`rounded-full border px-1.5 py-px ${DELIVERY_TONES[item.state ?? ''] || ''}`}>
                    {DELIVERY_LABELS[item.state ?? ''] || item.state}
                  </span>
                  {item.http_status ? <span className='text-muted-foreground'>HTTP {item.http_status}</span> : null}
                  {item.error ? <span className='text-muted-foreground'>{item.error}</span> : null}
                </div>
              ))}
              <p className='text-xs text-muted-foreground'>
                站内提醒入队即达；Webhook 最多重试 5 次。这里的「已重试」表示还在重试窗口内，「最终失败」表示重试次数已用尽或接收端拒绝。
              </p>
            </div>
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}

