import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest, newRequestKey } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useAPI, useAction } from '@/features/foundation/hooks'
import {
  Empty,
  Notices,
  QueryState,
  SelectField,
} from '@/features/foundation/ui'
import { formatMinute } from '@/features/playback/zone'

type Segment = Schema<'RecordingSegment'>
type Stats = Schema<'RecordingStats'>
type Outcome = Schema<'RecordingDeleteOutcome'>

const STATE_LABELS: Record<Segment['state'], string> = {
  finalizing: '写入中',
  ready: '可用',
  missing: '缺失',
  damaged: '损坏',
  conflict: '冲突',
  provisional: '待确认',
  deleted: '已删除',
}

function bytes(value: number) {
  if (value >= 1 << 30) return `${(value / (1 << 30)).toFixed(2)} GB`
  if (value >= 1 << 20) return `${(value / (1 << 20)).toFixed(1)} MB`
  if (value >= 1 << 10) return `${Math.round(value / (1 << 10))} KB`
  return `${value} B`
}

function duration(ms: number) {
  const total = Math.round(ms / 1000)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const seconds = total % 60
  return hours > 0
    ? `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
    : `${minutes}:${String(seconds).padStart(2, '0')}`
}

function localInput(date: Date) {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

// Turning a wall clock in the site zone into an instant is the playback page's
// job too; it gets that from zone.ts. Here the window is always "the last N
// days up to now", so the browser's own clock is enough and the displayed
// timestamps still use the site zone.
function rangeFor(days: number): { start: string; end: string } {
  const end = new Date()
  const start = new Date(end.getTime() - days * 24 * 3600_000)
  return { start: localInput(start), end: localInput(end) }
}

export function RecordingFiles({
  initialChannel = '',
  active = true,
}: {
  initialChannel?: string
  active?: boolean
}) {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const client = useQueryClient()
  const channels = useAPI<PageData<Schema<'Channel'>>>('/api/v1/channels?limit=100')
  const site = useAPI<Schema<'Site'>>('/api/v1/site')
  const pools = useAPI<PageData<Schema<'Pool'>>>('/api/v1/storage-pools?limit=100', admin)
  const [channel, setChannel] = useState(initialChannel)
  const [days, setDays] = useState('7')
  const [revision, setRevision] = useState('')
  const [pool, setPool] = useState('')
  const [health, setHealth] = useState('all')
  const [protection, setProtectionFilter] = useState('all')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [outcomes, setOutcomes] = useState<Outcome[] | null>(null)
  const action = useAction()

  const permitted = (channels.data?.items || []).filter((entry) =>
    entry.permissions.includes('playback')
  )
  const activeChannel = channel || permitted[0]?.id || ''
  const window = rangeFor(Number(days))
  const zone = site.data?.timezone || ''

  // One request for the page: the list and its totals describe exactly the same
  // filter window, so the summary can never disagree with the rows below it.
  const filters = useMemo(() => {
    const params = new URLSearchParams({
      channel_id: activeChannel,
      start: new Date(window.start).toISOString(),
      end: new Date(window.end).toISOString(),
      limit: '100',
    })
    if (revision) params.set('source_revision_id', revision)
    if (pool) params.set('pool_id', pool)
    if (health === 'ready') params.append('state', 'ready')
    if (health === 'anomalous') {
      params.append('state', 'missing')
      params.append('state', 'damaged')
      params.append('state', 'conflict')
    }
    if (health === 'deleted') params.append('state', 'deleted')
    if (protection !== 'all') params.set('protected', protection === 'yes' ? 'true' : 'false')
    return params.toString()
    // window.start/end are derived from the day count, so days is the real input.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeChannel, days, revision, pool, health, protection])

  const list = useAPI<{ items: Segment[]; next_cursor: string | null }>(
    `/api/v1/recordings?${filters}`,
    active && !!activeChannel
  )
  const stats = useAPI<Stats>(
    `/api/v1/recordings/stats?${filters}`,
    active && !!activeChannel
  )
  // Source revisions label the filter; the recordings themselves only carry the
  // revision id.
  const revisions = useAPI<Schema<'RevisionPage'>>(
    `/api/v1/channels/${activeChannel}/source-revisions?limit=100`,
    active && !!activeChannel
  )

  if (!permitted.length && channels.data)
    return <Empty>没有可管理的通道，请联系管理员分配录像回放权限。</Empty>
  if (!admin)
    return (
      <Empty>
        录像文件管理只对管理员开放：删除与保护都会改变证据状态。
      </Empty>
    )

  const items = list.data?.items || []
  const selectable = items.filter(
    (item) => item.state === 'ready' || item.state === 'deleted'
  )
  const chosen = items.filter((item) => selected.has(item.id))
  const allChecked =
    selectable.length > 0 && selectable.every((item) => selected.has(item.id))
  const willDelete = chosen.filter((item) => item.state === 'ready')
  const protectedChosen = chosen.filter((item) => item.protected)
  const expectedBytes = willDelete
    .filter((item) => !item.protected)
    .reduce((sum, item) => sum + item.bytes, 0)

  function toggle(id: string, checked: boolean) {
    setSelected((previous) => {
      const next = new Set(previous)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  // One place decides what "succeeded" means for this page. The batch delete
  // answers with its own per-row summary, so it may return either a fixed
  // message or one derived from the response.
  async function run<T>(
    work: () => Promise<T>,
    message: string | ((value: T) => string)
  ) {
    setOutcomes(null)
    // The batch delete reports its own outcome per row, so it returns the text
    // it wants shown rather than making the caller re-derive it from the rows.
    let summary = typeof message === 'string' ? message : ''
    const ok = await action.run(async () => {
      const value = await work()
      if (typeof message === 'function') summary = message(value)
      return value
    }, summary)
    if (ok) {
      setSelected(new Set())
      await client.invalidateQueries()
    }
  }

  async function setProtection(item: Segment, protect: boolean) {
    await run(
      () =>
        apiRequest(
          `/api/v1/recordings/${item.id}`,
          jsonRequest('PATCH', { protected: protect })
        ),
      protect ? '已保护该录像' : '已解除保护'
    )
  }

  async function deleteSelected() {
    setConfirmDelete(false)
    await run(async () => {
      const init = jsonRequest('POST', { segment_ids: [...selected] })
      const result = await apiRequest<{
        items: Outcome[]
        released_bytes: number
      }>('/api/v1/recordings/batch-delete-local', {
        ...init,
        headers: { ...init.headers, 'Idempotency-Key': newRequestKey() },
      })
      setOutcomes(result.items)
      const removed = result.items.filter((entry) => entry.state === 'deleted')
      const kept = result.items.length - removed.length
      return {
        kept,
        text: kept
          ? `${removed.length} 段已删除（释放 ${bytes(result.released_bytes)}），${kept} 段被保留，理由见下表`
          : `${removed.length} 段已删除，释放 ${bytes(result.released_bytes)}`,
      }
    }, (value) => value.text)
  }

  return (
    <div className='space-y-4'>
      <QueryState
        pending={channels.isPending || site.isPending}
        error={channels.error || site.error}
        retry={() => {
          void channels.refetch()
          void site.refetch()
        }}
      />
      {!!permitted.length && (
        <>
          <RetentionPanel site={site.data} stats={stats.data} />
          <form
            className='grid items-end gap-3 rounded-xl border bg-card p-4 md:grid-cols-5'
            onSubmit={(event) => event.preventDefault()}
          >
            <SelectField
              label='通道'
              value={activeChannel}
              onValueChange={(next) => {
                setChannel(next)
                setSelected(new Set())
                setRevision('')
              }}
              options={permitted.map((entry) => ({
                value: entry.id,
                label: `CH${String(entry.channel_no).padStart(2, '0')} · ${entry.channel_name}`,
              }))}
            />
            <SelectField
              label='时间范围'
              value={days}
              onValueChange={(next) => {
                setDays(next)
                setSelected(new Set())
              }}
              options={[
                { value: '1', label: '最近 24 小时' },
                { value: '7', label: '最近 7 天' },
                { value: '30', label: '最近 30 天' },
                { value: '90', label: '最近 90 天' },
              ]}
            />
            <SelectField
              label='来源版本'
              value={revision}
              onValueChange={(next) => {
                setRevision(next)
                setSelected(new Set())
              }}
              options={[
                { value: '', label: '全部历史源' },
                ...(revisions.data?.items || []).map((entry) => ({
                  value: entry.id,
                  label: `修订 ${entry.number} · ${entry.config.ip}`,
                })),
              ]}
            />
            <SelectField
              label='存储池'
              value={pool}
              onValueChange={(next) => setPool(next)}
              options={[
                { value: '', label: '全部存储池' },
                ...(pools.data?.items || []).map((entry) => ({
                  value: entry.id,
                  label: entry.name,
                })),
              ]}
            />
            <SelectField
              label='文件健康'
              value={health}
              onValueChange={(next) => setHealth(next)}
              options={[
                { value: 'all', label: '全部' },
                { value: 'ready', label: '仅可用' },
                { value: 'anomalous', label: '仅异常' },
                { value: 'deleted', label: '仅已删除' },
              ]}
            />
            <SelectField
              label='保护状态'
              value={protection}
              onValueChange={setProtectionFilter}
              options={[
                { value: 'all', label: '全部' },
                { value: 'yes', label: '仅已保护' },
                { value: 'no', label: '仅未保护' },
              ]}
            />
          </form>
          <Notices error={action.error} notice={action.notice} />
          {outcomes && <OutcomeTable outcomes={outcomes} />}
          <div className='flex flex-wrap items-center justify-between gap-3'>
            <span className='text-xs text-muted-foreground'>
              {items.length} 段{selected.size > 0 ? ` · 已选 ${selected.size} 段` : ''}
              {stats.data ? ` · 筛选范围内共 ${stats.data.segments} 段` : ''}
            </span>
            {selected.size > 0 && (
              <div className='flex items-center gap-2'>
                <Button
                  type='button'
                  variant='ghost'
                  onClick={() => setSelected(new Set())}
                >
                  取消选择
                </Button>
                <Button
                  type='button'
                  variant='destructive'
                  disabled={action.pending || !willDelete.length}
                  onClick={() => setConfirmDelete(true)}
                >
                  删除本地副本（{willDelete.length} 段）
                </Button>
              </div>
            )}
          </div>
          <QueryState
            pending={list.isPending}
            error={list.error}
            retry={() => list.refetch()}
          />
          {list.data && (
            <div className='overflow-x-auto rounded-xl border bg-card'>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className='w-10'>
                      <Checkbox
                        aria-label='选择当前结果'
                        checked={allChecked}
                        disabled={!selectable.length}
                        onCheckedChange={(state) => {
                          setSelected(
                            state === true
                              ? new Set(selectable.map((item) => item.id))
                              : new Set()
                          )
                        }}
                      />
                    </TableHead>
                    <TableHead>时间</TableHead>
                    <TableHead>时长</TableHead>
                    <TableHead>大小</TableHead>
                    <TableHead>媒体</TableHead>
                    <TableHead>状态</TableHead>
                    <TableHead>保护</TableHead>
                    <TableHead className='text-right'>操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {items.map((item) => (
                    <TableRow key={item.id} data-testid='recording-row'>
                      <TableCell>
                        <Checkbox
                          aria-label={`选择 ${item.id}`}
                          checked={selected.has(item.id)}
                          // A file that is still being written, or one that
                          // already vanished, is not something to delete here.
                          disabled={item.state !== 'ready'}
                          onCheckedChange={(state) =>
                            toggle(item.id, state === true)
                          }
                        />
                      </TableCell>
                      <TableCell className='font-mono text-xs'>
                        {zone ? formatMinute(Date.parse(item.start), zone) : '—'}
                      </TableCell>
                      <TableCell className='text-sm tabular-nums'>
                        {duration(Date.parse(item.end) - Date.parse(item.start))}
                      </TableCell>
                      <TableCell className='text-sm tabular-nums'>
                        {bytes(item.bytes)}
                      </TableCell>
                      <TableCell className='text-xs text-muted-foreground'>
                        {item.media.codec
                          ? `${item.media.width}×${item.media.height} · ${item.media.codec}`
                          : '—'}
                      </TableCell>
                      <TableCell className='text-sm'>
                        {STATE_LABELS[item.state] || item.state}
                        {item.check_state && item.check_state !== item.state && (
                          <span className='ml-1 text-xs text-muted-foreground'>
                            （{item.check_state}）
                          </span>
                        )}
                      </TableCell>
                      <TableCell className='text-sm'>
                        {item.protected ? '已保护' : '—'}
                      </TableCell>
                      <TableCell className='text-right'>
                        <div className='flex items-center justify-end gap-2'>
                          {item.state === 'ready' && (
                            <a
                              href={`/api/v1/recordings/${item.id}/content`}
                              className='text-xs underline underline-offset-4'
                            >
                              下载
                            </a>
                          )}
                          <Button
                            type='button'
                            variant='ghost'
                            size='sm'
                            aria-label={`${item.protected ? '解除保护' : '保护'} ${
                              zone ? formatMinute(Date.parse(item.start), zone) : item.id
                            }`}
                            disabled={action.pending || item.state === 'deleted'}
                            onClick={() => void setProtection(item, !item.protected)}
                          >
                            {item.protected ? '解除保护' : '保护'}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              {!items.length && (
                <div className='p-8 text-center text-sm text-muted-foreground'>
                  该范围内没有录像
                </div>
              )}
            </div>
          )}
        </>
      )}
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`删除 ${willDelete.length} 段录像的本地副本？`}
        desc={`将释放约 ${bytes(expectedBytes)}。录像索引与缺口证据保留，这些录像将无法播放；${
          protectedChosen.length
            ? `已保护的 ${protectedChosen.length} 段会被跳过。`
            : '没有已保护的录像。'
        }`}
        confirmText='删除本地副本'
        cancelBtnText='取消'
        destructive
        isLoading={action.pending}
        handleConfirm={() => void deleteSelected()}
      />
    </div>
  )

}

// The per-row result is the contract: an operator must see which files were
// removed and why the rest were kept, never a single "done" that hides the
// difference (PRD FILE-05).
function OutcomeTable({ outcomes }: { outcomes: Outcome[] }) {
  const LABELS: Record<Outcome['state'], string> = {
    deleted: '已删除',
    protected: '已保护，已跳过',
    not_ready: '尚未完成发布',
    missing: '文件已不在，未做更改',
    in_use: '正在写入或播放，已跳过',
    not_found: '未找到',
  }
  return (
    <div className='overflow-x-auto rounded-xl border bg-card'>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>录像</TableHead>
            <TableHead>结果</TableHead>
            <TableHead className='text-right'>释放</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {outcomes.map((outcome) => (
            <TableRow key={outcome.segment_id} data-testid='delete-outcome'>
              <TableCell className='font-mono text-xs'>
                {outcome.segment_id.slice(0, 8)}
              </TableCell>
              <TableCell className='text-sm'>
                {LABELS[outcome.state] || outcome.state}
                {outcome.reason && outcome.state !== 'deleted' && (
                  <span className='ml-1 text-xs text-muted-foreground'>
                    （{outcome.reason}）
                  </span>
                )}
              </TableCell>
              <TableCell className='text-right text-sm tabular-nums'>
                {outcome.bytes ? bytes(outcome.bytes) : '—'}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

// Retention is the one setting that deletes footage without anyone present, so
// the panel states the current occupancy and what the period would keep before
// the operator turns it on.
function RetentionPanel({
  site,
  stats,
}: {
  site?: Schema<'Site'>
  stats?: Stats
}) {
  const action = useAction()
  const [days, setDays] = useState(site ? String(site.recording_retention_days) : '0')
  if (!site) return null
  const enabled = site.recording_retention_days > 0
  return (
    <div className='rounded-xl border bg-card p-4'>
      <div className='flex flex-wrap items-start justify-between gap-3'>
        <div className='min-w-0'>
          <p className='text-sm font-medium'>本地保留策略</p>
          <p className='mt-1 text-sm text-muted-foreground'>
            {enabled
              ? `超过 ${site.recording_retention_days} 天的录像会自动删除本地副本；已保护的录像始终跳过。`
              : '当前不自动删除任何录像。设置天数后，超过该期限的录像会自动删除本地副本（已保护的除外）。'}
          </p>
          {stats && (
            <p className='mt-1 text-xs text-muted-foreground'>
              当前筛选范围：{stats.segments} 段 · {bytes(stats.logical_bytes)} 可播放 ·
              已删除释放 {bytes(stats.deleted_bytes)}
            </p>
          )}
        </div>
        <div className='flex items-end gap-2'>
          <SelectField
            label='保留天数'
            ariaLabel='保留天数'
            value={days}
            onValueChange={setDays}
            options={[
              { value: '0', label: '不自动删除' },
              { value: '1', label: '1 天' },
              { value: '3', label: '3 天' },
              { value: '7', label: '7 天' },
              { value: '14', label: '14 天' },
              { value: '30', label: '30 天' },
              { value: '90', label: '90 天' },
              { value: '180', label: '180 天' },
              { value: '365', label: '365 天' },
            ]}
          />
          <Button
            type='button'
            variant='outline'
            disabled={action.pending}
            onClick={() =>
              void action.run(
                () =>
                  apiRequest(
                    '/api/v1/site',
                    jsonRequest(
                      'PATCH',
                      { recording_retention_days: Number(days) },
                      site.version
                    )
                  ),
                Number(days) > 0 ? '已启用本地保留策略' : '已关闭自动删除'
              )
            }
          >
            保存保留策略
          </Button>
        </div>
      </div>
      <Notices {...action} />
    </div>
  )
}
