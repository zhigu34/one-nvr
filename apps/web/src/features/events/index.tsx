import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
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
import { StatusBadge, type StatusTone } from '@/features/foundation/status-badge'
import { CardsSkeleton } from '@/features/foundation/skeletons'
import { Empty, QueryState, SelectField } from '@/features/foundation/ui'
import { formatClock, formatMinute, wallClockInput } from '@/features/playback/zone'

type Event = Schema<'Event'>
type Channel = Schema<'Channel'>

const HANDLE_LABELS: Record<string, string> = {
  open: '未处理',
  acknowledged: '已确认',
  closed: '已关闭',
  false_positive: '误报',
}

/** The one place mapping handling state to the shared badge vocabulary. */
const HANDLE_TONES: Record<string, StatusTone> = {
  open: 'warning',
  acknowledged: 'success',
  closed: 'neutral',
  false_positive: 'info',
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

/**
 * M3 batch 1: the event center (EVT-01/02/03). List + snapshot cards, a
 * detail drawer with the handling trail, and a jump into playback for the
 * event window. Media states are shown separately from handling state — an
 * event without a retained snapshot is not a handled event.
 */
export function Events() {
  const site = useAPI<Schema<'Site'>>('/api/v1/site')
  const zone = site.data?.timezone || ''
  const active = !!zone
  const channels = useAPI<PageData<Channel>>('/api/v1/channels?limit=100', active)
  const permitted = (channels.data?.items || []).filter((channel) =>
    channel.permissions.includes('playback')
  )
  const [channel, setChannel] = useState('')
  const [category, setCategory] = useState('')
  const [handleState, setHandleState] = useState('')
  const [selected, setSelected] = useState<string>('')
  const [openingEvent, setOpeningEvent] = useState<string>('')
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

  if (!permitted.length && channels.data)
    return <Empty>没有可查看事件的通道，请联系管理员分配录像回放权限。</Empty>

  return (
    <div className='space-y-4'>
      <div>
        <h1 className='text-2xl font-semibold tracking-tight'>事件</h1>
        <p className='mt-1 text-sm text-muted-foreground'>
          智能检测事件（最近 7 天）。事件索引保留 90 天，抓拍与录像按各自的保留策略；点击事件可查看抓拍并跳到对应录像。
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
          <div className='flex flex-wrap items-end gap-3 rounded-xl border bg-card p-4'>
            <SelectField
              label='通道'
              value={channel}
              onValueChange={(next) => setChannel(next ?? '')}
              options={[
                { value: '', label: '全部通道' },
                ...permitted.map((channel) => ({
                  value: channel.id ?? '',
                  label: `CH${String(channel.channel_no ?? 0).padStart(2, '0')} ${channel.channel_name}`,
                })),
              ]}
              className='w-44'
            />
            <SelectField
              label='类别'
              value={category}
              onValueChange={(next) => setCategory(next ?? '')}
              options={[
                { value: '', label: '全部' },
                ...Object.entries(CATEGORY_LABELS).map(([value, label]) => ({ value, label })),
              ]}
              className='w-28'
            />
            <SelectField
              label='处理状态'
              value={handleState}
              onValueChange={(next) => setHandleState(next ?? '')}
              options={[
                { value: '', label: '全部' },
                ...Object.entries(HANDLE_LABELS).map(([value, label]) => ({ value, label })),
              ]}
              className='w-32'
            />
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
          <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-3'>
            {items.map((item) => (
              <button
                key={item.id}
                type='button'
                data-testid='event-card'
                className='group space-y-2 rounded-xl border bg-card p-3 text-left text-sm transition-colors hover:bg-accent hover:shadow-sm'
                onClick={() => setSelected(item.id ?? '')}
              >
                <div className='flex items-center justify-between'>
                  <span className='font-medium'>
                    {CATEGORY_LABELS[item.category ?? ''] || item.category}
                    <span className='ml-2 text-xs text-muted-foreground'>
                      {Math.round((item.score ?? 0) * 100)}%
                    </span>
                  </span>
                  <StatusBadge tone={HANDLE_TONES[item.handle_state ?? 'open'] || 'neutral'}>
                    {HANDLE_LABELS[item.handle_state ?? 'open']}
                  </StatusBadge>
                </div>
                {item.snapshot_state === 'ready' ? (
                  <img
                    src={`/api/v1/events/${item.id}/snapshot`}
                    alt='事件抓拍'
                    loading='lazy'
                    onError={() => setOpeningEvent(item.id ?? '')}
                    className='aspect-video w-full rounded object-cover transition-transform duration-200 group-hover:scale-[1.02]'
                  />
                ) : (
                  <div className='grid aspect-video w-full place-items-center rounded bg-muted text-xs text-muted-foreground'>
                    {item.snapshot_state === 'pending' ? '抓拍生成中' : '抓拍不可用'}
                  </div>
                )}
                <p className='text-xs text-muted-foreground' title={formatClock(Date.parse(item.started_at ?? ''), zone)}>
                  {formatClock(Date.parse(item.started_at ?? ''), zone)}
                  {item.ended_at
                    ? ` · 持续 ${Math.max(1, Math.round((Date.parse(item.ended_at) - Date.parse(item.started_at ?? '')) / 1000))} 秒`
                    : ' · 进行中'}
                  {item.zones?.length ? ` · ${item.zones.join('/')}` : ''}
                </p>
              </button>
            ))}
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
  const item = event.data

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
                  : item.snapshot_state === 'pending'
                    ? '抓拍生成中'
                    : '抓拍已不可用'}
              </div>
            )}
            <div className='grid grid-cols-2 gap-2 rounded-lg border bg-muted/40 p-3'>
              <span className='text-muted-foreground'>开始</span>
              <span>{formatClock(Date.parse(item.started_at ?? ''), zone)}</span>
              <span className='text-muted-foreground'>结束</span>
              <span>
                {item.ended_at ? formatClock(Date.parse(item.ended_at), zone) : '进行中'}
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
