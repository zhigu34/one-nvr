import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { Empty, Notices, QueryState, SelectField } from '@/features/foundation/ui'

type Subscription = Schema<'OperationalSubscription'>
type Channel = Schema<'NotificationChannel'>

/**
 * What an operator is told about the system, as opposed to what a camera saw.
 *
 * Detection rules answer "did someone walk past"; these answer "is the system
 * healthy". Until this panel existed, a camera could be offline for a weekend with
 * every page showing the row and nobody told — the product noticed and could not
 * speak.
 *
 * One row per fault kind, with the destinations that hear about it. The kinds are
 * the granularity an operator thinks in: a camera offline belongs in the duty
 * group at 3am, a queue backlog probably does not.
 */
const KIND_TEXT: Record<string, { label: string; help: string }> = {
  camera_offline: { label: '摄像头离线', help: '通道的源连不上：可能是断电、断网或摄像头故障。' },
  stream_degraded: { label: '码流异常', help: '摄像头在线，但主流或子流不正常。' },
  recording_stalled: { label: '录像中断', help: '流正常，但分片不再落盘——录像正在丢。' },
  storage_unwritable: { label: '存储不可写', help: '存储池拒绝写入：盘满、只读或挂载掉了。' },
  capacity_low: { label: '容量不足', help: '可用空间低于工作线，即将停止录像。' },
  detector_unavailable: { label: '检测器不可达', help: 'Frigate 无响应：所有通道同时失去检测。' },
  broker_unavailable: { label: '消息服务不可达', help: 'MQTT 断开：检测事件进不来。' },
  config_publish_failed: { label: '配置发布失败', help: '检测配置没有生效，检测器仍在用旧配置。' },
  queue_backlog: { label: '检测队列积压', help: '检测处理跟不上，事件会延迟。' },
}

const SEVERITY_OPTIONS = [
  { value: 'critical', label: '严重' },
  { value: 'warning', label: '警告及以上' },
  { value: 'info', label: '全部' },
]

export function OperationalSubscriptions() {
  const client = useQueryClient()
  const subscriptions = useAPI<{ items: Subscription[]; kinds: string[] }>(
    '/api/v1/notification-channels/operational'
  )
  const channels = useAPI<{ items: Channel[]; kinds: string[] }>(
    '/api/v1/notification-channels'
  )
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [pending, setPending] = useState('')

  // Only destinations that can actually carry a message: the inbox is always
  // there, but an operator reading this page is choosing external channels.
  const destinations = useMemo(
    () => (channels.data?.items || []).filter((channel) => channel.enabled),
    [channels.data]
  )
  const byKind = useMemo(() => {
    const map = new Map<string, Subscription[]>()
    for (const item of subscriptions.data?.items || []) {
      const list = map.get(item.kind) || []
      list.push(item)
      map.set(item.kind, list)
    }
    return map
  }, [subscriptions.data])
  const kinds = subscriptions.data?.kinds?.length
    ? subscriptions.data.kinds
    : Object.keys(KIND_TEXT)

  async function save(kind: string, items: { channel_id: string; min_severity: string }[]) {
    setPending(kind)
    setError('')
    setNotice('')
    try {
      await apiRequest(
        `/api/v1/notification-channels/operational/${kind}`,
        jsonRequest('PUT', { items })
      )
      setNotice(`${KIND_TEXT[kind]?.label ?? kind}：已保存`)
      await client.invalidateQueries()
    } catch (cause) {
      setError(apiErrorText(cause))
    } finally {
      setPending('')
    }
  }

  return (
    <div className='space-y-3 rounded-xl border bg-card p-4'>
      <div>
        <h2 className='text-lg font-semibold tracking-tight'>运维通知</h2>
        <p className='mt-1 text-sm text-muted-foreground'>
          系统出故障时通知谁。与上面的检测规则分开：那些回答「摄像头看到了什么」，这里回答「系统是否正常」。
          同一故障只发一次开始与一次恢复，不会反复打扰。
        </p>
      </div>
      <QueryState
        pending={subscriptions.isPending || channels.isPending}
        error={subscriptions.error || channels.error}
        retry={() => void subscriptions.refetch()}
      />
      <Notices notice={notice} error={error} />
      {!destinations.length && channels.data && (
        <Empty>先在上面添加一个通知渠道，才能指定故障通知发给谁。</Empty>
      )}
      <div className='space-y-2'>
        {kinds.map((kind) => {
          const text = KIND_TEXT[kind] ?? { label: kind, help: '' }
          const current = byKind.get(kind) || []
          return (
            <KindRow
              key={kind}
              kind={kind}
              label={text.label}
              help={text.help}
              current={current}
              destinations={destinations}
              pending={pending === kind}
              onSave={(items) => void save(kind, items)}
            />
          )
        })}
      </div>
    </div>
  )
}

/**
 * One fault kind. The destinations are edited as a set — add and remove here, then
 * save — because "camera offline goes to these two" is one decision.
 */
function KindRow({
  kind,
  label,
  help,
  current,
  destinations,
  pending,
  onSave,
}: {
  kind: string
  label: string
  help: string
  current: Subscription[]
  destinations: Channel[]
  pending: boolean
  onSave: (items: { channel_id: string; min_severity: string }[]) => void
}) {
  // The draft follows the stored set until the operator edits it.
  //
  // A `useState` initialiser runs once, and this component mounts before the
  // subscription query resolves — so seeding from `current` showed every kind as
  // "不通知" even when it had destinations. The echo is the fallback, not an
  // override: once `dirty` is set the operator's own edit wins.
  const [draft, setDraft] = useState<{ channel_id: string; min_severity: string }[]>([])
  const [dirty, setDirty] = useState(false)
  const effective = dirty
    ? draft
    : current.map((item) => ({ channel_id: item.channel_id, min_severity: item.min_severity }))

  function update(next: { channel_id: string; min_severity: string }[]) {
    setDraft(next)
    setDirty(true)
  }

  const available = destinations.filter(
    (channel) => !effective.some((item) => item.channel_id === channel.id)
  )
  const nameOf = (channelID: string) =>
    destinations.find((channel) => channel.id === channelID)?.name ?? channelID.slice(0, 8)

  return (
    <div
      data-testid='operational-kind'
      data-kind={kind}
      className='rounded-lg border bg-card/60 p-2.5'
    >
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='min-w-0'>
          <span className='text-sm font-medium'>{label}</span>
          {effective.length === 0 && (
            <span className='ml-2 text-xs text-muted-foreground'>不通知</span>
          )}
        </div>
        <div className='flex items-center gap-2'>
          <SelectField
            ariaLabel={`${label} 新增通知渠道`}
            value=''
            onValueChange={(next) => {
              if (!next) return
              update([...effective, { channel_id: next, min_severity: 'warning' }])
            }}
            options={[
              { value: '', label: '添加渠道…' },
              ...available.map((channel) => ({ value: channel.id ?? '', label: channel.name ?? '' })),
            ]}
            className='w-40'
            triggerClassName='h-8'
          />
          <Button
            type='button'
            size='sm'
            disabled={!dirty || pending}
            onClick={() => {
              onSave(effective)
              setDirty(false)
            }}
          >
            {pending ? '保存中…' : '保存'}
          </Button>
        </div>
      </div>
      {help && <p className='mt-1 text-[11px] text-muted-foreground'>{help}</p>}
      {effective.length > 0 && (
        <div className='mt-2 space-y-1'>
          {effective.map((item, index) => (
            <div key={item.channel_id} className='flex flex-wrap items-center gap-2 text-xs'>
              <span className='w-32 truncate'>{nameOf(item.channel_id)}</span>
              <SelectField
                ariaLabel={`${label} ${nameOf(item.channel_id)} 最低级别`}
                value={item.min_severity}
                onValueChange={(next) => {
                  const updated = [...effective]
                  updated[index] = { ...item, min_severity: next || 'warning' }
                  update(updated)
                }}
                options={SEVERITY_OPTIONS}
                className='w-32'
                triggerClassName='h-7'
              />
              <Button
                type='button'
                size='sm'
                variant='outline'
                className='h-7 border-red-500/30 text-red-600 dark:text-red-400'
                onClick={() => update(effective.filter((_, at) => at !== index))}
              >
                移除
              </Button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
