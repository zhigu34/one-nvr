import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
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

function handleError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'event_version_conflict') return '该事件已被他人处理，请刷新后重试'
    const texts: Record<string, string> = {
      snapshot_pending: '抓拍尚未生成，稍后再看',
      snapshot_unavailable: '抓拍暂时不可读',
    }
    if (texts[error.code]) return texts[error.code]
    return error.message
  }
  return error instanceof Error ? error.message : '操作失败'
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
  // The 7-day window is anchored once per mount: re-anchoring it on every
  // render would refetch the list continuously.
  const [window] = useState(() => {
    const end = Date.now()
    return { start: end - 7 * 86_400_000, end }
  })
  const activeChannel = channel || permitted[0]?.id || ''
  const filters = (() => {
    const params = new URLSearchParams({
      start: String(window.start),
      end: String(window.end),
      limit: '50',
    })
    if (activeChannel) params.set('channel_id', activeChannel)
    if (category) params.set('category', category)
    if (handleState) params.set('handle_state', handleState)
    return params.toString()
  })()
  const events = useAPI<Schema<'EventPage'>>(
    `/api/v1/events?${filters}`,
    active && !!activeChannel
  )
  const items = events.data?.items || []

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
              value={activeChannel}
              onValueChange={(next) => setChannel(next ?? '')}
              options={permitted.map((channel) => ({
                value: channel.id ?? '',
                label: `CH${String(channel.channel_no ?? 0).padStart(2, '0')} ${channel.channel_name}`,
              }))}
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
          <QueryState pending={events.isPending} error={events.error} retry={events.refetch} />
          {events.data && !items.length && <Empty>该筛选范围内没有事件。</Empty>}
          <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-3'>
            {items.map((item) => (
              <button
                key={item.id}
                type='button'
                data-testid='event-card'
                className='space-y-2 rounded-xl border bg-card p-3 text-left text-sm transition-colors hover:bg-accent'
                onClick={() => setSelected(item.id ?? '')}
              >
                <div className='flex items-center justify-between'>
                  <span className='font-medium'>
                    {CATEGORY_LABELS[item.category ?? ''] || item.category}
                    <span className='ml-2 text-xs text-muted-foreground'>
                      {Math.round((item.score ?? 0) * 100)}%
                    </span>
                  </span>
                  <span
                    className={
                      item.handle_state === 'open'
                        ? 'text-xs text-amber-600'
                        : 'text-xs text-muted-foreground'
                    }
                  >
                    {HANDLE_LABELS[item.handle_state ?? 'open']}
                  </span>
                </div>
                {item.snapshot_state === 'ready' ? (
                  <img
                    src={`/api/v1/events/${item.id}/snapshot`}
                    alt='事件抓拍'
                    loading='lazy'
                    onError={() => setOpeningEvent(item.id ?? '')}
                    className='aspect-video w-full rounded object-cover'
                  />
                ) : (
                  <div className='grid aspect-video w-full place-items-center rounded bg-muted text-xs text-muted-foreground'>
                    {item.snapshot_state === 'pending' ? '抓拍生成中' : '抓拍不可用'}
                  </div>
                )}
                <p className='text-xs text-muted-foreground'>
                  {formatClock(Date.parse(item.started_at ?? ''), zone)}
                  {item.ended_at
                    ? ` · 持续 ${Math.max(1, Math.round((Date.parse(item.ended_at) - Date.parse(item.started_at ?? '')) / 1000))} 秒`
                    : ' · 进行中'}
                  {item.zones?.length ? ` · ${item.zones.join('/')}` : ''}
                </p>
              </button>
            ))}
          </div>
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
  const item = event.data

  async function transition(state: string) {
    if (!item) return
    setPending(true)
    setError('')
    try {
      const init = jsonRequest('PATCH', { state, note, version: item.version ?? 0 })
      await apiRequest(`/api/v1/events/${eventID}`, init)
      setNote('')
      await client.invalidateQueries()
    } catch (cause) {
      setError(handleError(cause))
    } finally {
      setPending(false)
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
              <div className='flex flex-wrap gap-2'>
                <Button size='sm' disabled={pending} onClick={() => void transition('acknowledged')}>
                  已确认
                </Button>
                <Button size='sm' variant='outline' disabled={pending} onClick={() => void transition('closed')}>
                  已关闭
                </Button>
                <Button size='sm' variant='outline' disabled={pending} onClick={() => void transition('false_positive')}>
                  误报
                </Button>
                <Button size='sm' variant='ghost' disabled={pending} onClick={() => void transition('open')}>
                  重新打开
                </Button>
              </div>
            </div>
            <div className='space-y-2'>
              <p className='font-medium'>处理记录</p>
              {!item.handling?.length && (
                <p className='text-xs text-muted-foreground'>暂无处理记录。</p>
              )}
              {(item.handling || []).map((record) => (
                <p key={record.id} className='text-xs text-muted-foreground'>
                  {formatClock(Date.parse(record.created_at ?? ''), zone)} · {record.actor_name} ·{' '}
                  {HANDLE_LABELS[record.state ?? ''] || record.state}
                  {record.note ? ` · ${record.note}` : ''}
                </p>
              ))}
            </div>
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}
