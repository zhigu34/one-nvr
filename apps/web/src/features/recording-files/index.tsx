import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Download, Shield, ShieldOff, Trash2 } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest, newRequestKey } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { useAPI, useAction } from '@/features/foundation/hooks'
import {
  Empty,
  Notices,
  QueryState,
  SelectField,
} from '@/features/foundation/ui'
import { cn } from '@/lib/utils'
import { formatMinute } from '@/features/playback/zone'
import { ExportsDrawer } from '@/features/recording-exports/exports-drawer'

type Target = Schema<'ArchiveTarget'>

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
  // The workspace shell owns the active channel; the panel no longer asks for
  // it, so the channel button shows the same channel as the playback above.
  controlledChannelId = '',
  // The shell's playback search range is the panel's single time source —
  // there is no second time picker here to disagree with the toolbar. Falls
  // back to the last 7 days when the shell has not reported yet.
  controlledRange,
}: {
  initialChannel?: string
  active?: boolean
  controlledChannelId?: string
  controlledRange?: { start: number; end: number }
}) {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const client = useQueryClient()
  const channels = useAPI<PageData<Schema<'Channel'>>>('/api/v1/channels?limit=100')
  const site = useAPI<Schema<'Site'>>('/api/v1/site')
  const pools = useAPI<PageData<Schema<'Pool'>>>('/api/v1/storage-pools?limit=100', admin)
  const archiveTargets = useAPI<PageData<Target>>('/api/v1/archive-targets', admin)
  const [revision, setRevision] = useState('')
  const [pool, setPool] = useState('')
  const [health, setHealth] = useState('all')
  const [protection, setProtectionFilter] = useState('all')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [exportsOpen, setExportsOpen] = useState(false)
  const [archiveTarget, setArchiveTarget] = useState('')
  const [outcomes, setOutcomes] = useState<Outcome[] | null>(null)
  const action = useAction()

  const permitted = (channels.data?.items || []).filter((entry) =>
    entry.permissions.includes('playback')
  )
  const activeChannel = controlledChannelId || initialChannel || permitted[0]?.id || ''
  const exportable = permitted.find(
    (entry) => entry.id === activeChannel && entry.permissions.includes('export')
  )
  const window = controlledRange ?? rangeFor(7)
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
  }, [activeChannel, window, revision, pool, health, protection])

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

  async function archiveSelected() {
    if (!archiveTarget) return
    await run(async () => {
      const init = jsonRequest('POST', { target_id: archiveTarget, segment_ids: [...selected] })
      await apiRequest('/api/v1/recordings/archive', {
        ...init,
        headers: { ...init.headers, 'Idempotency-Key': newRequestKey() },
      })
    }, `已提交 ${selected.size} 段的上传任务`)
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
    <div className='flex min-h-0 flex-1 flex-col gap-3 text-video-chrome-foreground'>
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
          <form
            className='grid grid-cols-2 gap-2 rounded-lg border border-video-border bg-video-surface-raised p-2.5'
            onSubmit={(event) => event.preventDefault()}
          >
            <SelectField
              ariaLabel='保护状态'
              value={protection}
              onValueChange={setProtectionFilter}
              options={[
                { value: 'all', label: '全部保护' },
                { value: 'yes', label: '仅已保护' },
                { value: 'no', label: '仅未保护' },
              ]}
            />
            <SelectField
              ariaLabel='来源版本'
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
              ariaLabel='存储池'
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
              ariaLabel='文件健康'
              className='col-span-2'
              value={health}
              onValueChange={(next) => setHealth(next)}
              options={[
                { value: 'all', label: '全部健康' },
                { value: 'ready', label: '仅可用' },
                { value: 'anomalous', label: '仅异常' },
                { value: 'deleted', label: '仅已删除' },
              ]}
            />
          </form>
          <Notices error={action.error} notice={action.notice} />
          {outcomes && <OutcomeTable outcomes={outcomes} />}
          {/* Stats read as one quiet line; the toolbar below only exists when
              there is something to act on, so selecting never reflows the
              numbers. */}
          <div className='flex flex-wrap items-center justify-between gap-2 px-1 text-xs text-video-chrome-foreground/70'>
            <span>
              {items.length} 段{selected.size > 0 ? ` · 已选 ${selected.size} 段` : ''}
              {stats.data ? ` · 筛选范围内共 ${stats.data.segments} 段` : ''}
              {!!stats.data?.cloud_only && ` · 仅云端 ${stats.data.cloud_only} 段`}
            </span>
            {exportable && (
              <Button size='sm' variant='outline' onClick={() => setExportsOpen(true)}>
                导出记录
              </Button>
            )}
          </div>
          {selected.size > 0 && (
            <div className='sticky bottom-0 z-10 flex flex-wrap items-center gap-2 rounded-lg border border-amber-400/40 bg-video-surface-raised/95 px-3 py-2 shadow-lg backdrop-blur'>
              <span className='text-sm font-medium text-amber-400'>
                已选 {selected.size} 段
              </span>
              <div className='ml-auto flex flex-wrap items-center gap-1.5'>
                {admin && !!archiveTargets.data?.items.length && (
                  <>
                    <SelectField
                      ariaLabel='上传目标'
                      value={archiveTarget}
                      onValueChange={(next) => setArchiveTarget(next ?? '')}
                      options={[
                        { value: '', label: '选择归档目标' },
                        ...(archiveTargets.data?.items || [])
                          .filter((target) => target.enabled)
                          .map((target) => ({ value: target.id ?? '', label: target.name ?? '' })),
                      ]}
                      className='w-32'
                    />
                    <Button
                      type='button'
                      variant='outline'
                      size='sm'
                      disabled={action.pending || !archiveTarget}
                      onClick={() => void archiveSelected()}
                    >
                      上传云端（{selected.size}）
                    </Button>
                  </>
                )}
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  onClick={() => setSelected(new Set())}
                >
                  取消
                </Button>
                <Button
                  type='button'
                  variant='destructive'
                  size='sm'
                  disabled={action.pending || !willDelete.length}
                  onClick={() => setConfirmDelete(true)}
                >
                  删除本地副本（{willDelete.length} 段）
                </Button>
              </div>
            </div>
          )}
          <QueryState
            pending={list.isPending}
            error={list.error}
            retry={() => list.refetch()}
          />
          {list.data && (
            <div
              data-testid='files-rows'
              className='flex min-h-0 flex-1 flex-col gap-1.5 overflow-y-auto rounded-lg border border-video-border bg-video-surface-raised/40 p-2'
            >
              {selectable.length > 0 && (
                <div className='flex items-center gap-2 px-1 pb-1'>
                  <Checkbox
                    aria-label='选择当前结果'
                    checked={allChecked}
                    onCheckedChange={(state) =>
                      setSelected(
                        state === true
                          ? new Set(selectable.map((item) => item.id))
                          : new Set()
                      )
                    }
                  />
                  <span className='text-[11px] text-video-chrome-foreground/60'>
                    全选当前页（{selectable.length} 段可选）
                  </span>
                </div>
              )}
              {items.map((item) => (
                <RecordingRow
                  key={item.id}
                  item={item}
                  zone={zone}
                  actionPending={action.pending}
                  selected={selected.has(item.id)}
                  onToggle={(next) => toggle(item.id, next)}
                  onProtect={() => void setProtection(item, !item.protected)}
                />
              ))}
              {!items.length && (
                <div className='p-8 text-center text-sm text-video-chrome-foreground/60'>
                  该范围内没有录像
                </div>
              )}
            </div>
          )}
          {/* Retention deletes footage without anyone present, so it sits after
              the working area rather than pushing every filter below the fold;
              collapsed by default because it is rarely the daily concern. */}
          <Collapsible className='rounded-lg border border-video-border bg-video-surface-raised/40'>
            <CollapsibleTrigger asChild>
              <button
                type='button'
                className='flex w-full items-center justify-between gap-2 px-3 py-2 text-left text-sm text-video-chrome-foreground hover:bg-white/5'
              >
                <span className='flex items-center gap-2'>
                  <span className='inline-block h-2 w-2 rounded-sm bg-amber-400' />
                  本地保留策略
                </span>
                <ChevronDown className='size-4 transition-transform group-data-[state=open]:rotate-180' />
              </button>
            </CollapsibleTrigger>
            <CollapsibleContent className='border-t border-video-border p-3'>
              <RetentionPanel site={site.data} stats={stats.data} />
            </CollapsibleContent>
          </Collapsible>
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
      {exportable && (
        <ExportsDrawer
          channel={exportable}
          zone={zone}
          open={exportsOpen}
          onOpenChange={setExportsOpen}
        />
      )}
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
    <div className='flex flex-col gap-1.5 rounded-lg border border-video-border bg-video-surface-raised/40 p-2'>
      {outcomes.map((outcome) => (
        <div
          key={outcome.segment_id}
          data-testid='delete-outcome'
          className='flex items-center justify-between gap-3 rounded border border-video-border bg-video-surface px-2 py-1.5 text-xs'
        >
          <span className='font-mono text-video-chrome-foreground/70'>
            {outcome.segment_id.slice(0, 8)}
          </span>
          <span className='flex-1 truncate'>
            {LABELS[outcome.state] || outcome.state}
            {outcome.reason && outcome.state !== 'deleted' && (
              <span className='ml-1 text-video-chrome-foreground/50'>（{outcome.reason}）</span>
            )}
          </span>
          <span className='tabular-nums text-video-chrome-foreground/80'>
            {outcome.bytes ? bytes(outcome.bytes) : '—'}
          </span>
        </div>
      ))}
    </div>
  )
}

// One row in the right rail. Compact vertical layout: top line carries the
// time range and state badges, the bottom line carries media facts, the
// hover-only actions keep the panel quiet at rest.
function RecordingRow({
  item,
  zone,
  actionPending,
  selected,
  onToggle,
  onProtect,
}: {
  item: Segment
  zone: string
  actionPending: boolean
  selected: boolean
  onToggle: (next: boolean) => void
  onProtect: () => void
}) {
  const time = zone ? formatMinute(Date.parse(item.start), zone) : '—'
  const span = duration(Date.parse(item.end) - Date.parse(item.start))
  const media = item.media.codec
    ? `${item.media.width}×${item.media.height} · ${item.media.codec}`
    : '—'
  const stateLabel = STATE_LABELS[item.state] || item.state
  const fillable = item.state === 'ready'
  return (
    <div
      data-testid='recording-row'
      className={cn(
        'group/row relative grid grid-cols-[16px_1fr_auto] gap-2 rounded border px-2.5 py-2 text-xs',
        selected
          ? 'border-amber-400/60 bg-amber-400/10'
          : item.state === 'missing' || item.state === 'damaged' || item.state === 'conflict'
            ? 'border-red-500/30 bg-red-500/5 opacity-90'
            : 'border-video-border bg-video-surface hover:border-video-chrome-foreground/30'
      )}
    >
      <Checkbox
        aria-label={`选择 ${item.id}`}
        checked={selected}
        disabled={!fillable}
        onCheckedChange={(state) => onToggle(state === true)}
      />
      <div className='min-w-0 space-y-1'>
        <div className='flex flex-wrap items-center gap-x-2 gap-y-0.5'>
          <span className='font-mono text-[12px] text-video-chrome-foreground'>{time}</span>
          {item.protected && (
            <span className='rounded bg-amber-400/15 px-1.5 py-0.5 text-[10px] text-amber-400'>
              已保护
            </span>
          )}
          {item.archived && (
            <span className='rounded bg-emerald-400/15 px-1.5 py-0.5 text-[10px] text-emerald-400'>
              本地+云端
            </span>
          )}
          <span
            className={cn(
              'rounded px-1.5 py-0.5 text-[10px]',
              item.state === 'ready'
                ? 'bg-emerald-400/10 text-emerald-400'
                : item.state === 'deleted'
                  ? 'bg-video-chrome-foreground/5 text-video-chrome-foreground/40 line-through'
                  : 'bg-red-500/10 text-red-400'
            )}
          >
            {stateLabel}
          </span>
        </div>
        <div className='flex flex-wrap items-center gap-x-2 text-[10.5px] text-video-chrome-foreground/60'>
          <span className='tabular-nums'>{span}</span>
          <span className='text-video-chrome-foreground/30'>·</span>
          <span className='tabular-nums'>{bytes(item.bytes)}</span>
          <span className='text-video-chrome-foreground/30'>·</span>
          <span>{media}</span>
        </div>
      </div>
      <div className='flex items-center gap-1 opacity-0 transition-opacity group-hover/row:opacity-100'>
        {item.state === 'ready' && (
          <a
            href={`/api/v1/recordings/${item.id}/content`}
            aria-label={`下载 ${item.id}`}
            className='grid size-6 place-items-center rounded border border-video-border bg-video-surface-raised text-video-chrome-foreground/70 hover:text-video-chrome-foreground'
            title='下载'
          >
            <Download className='size-3.5' />
          </a>
        )}
        <button
          type='button'
          aria-label={`${item.protected ? '解除保护' : '保护'} ${time}`}
          disabled={actionPending || item.state === 'deleted'}
          onClick={onProtect}
          className='grid size-6 place-items-center rounded border border-video-border bg-video-surface-raised text-video-chrome-foreground/70 hover:text-amber-400 disabled:opacity-30'
          title={item.protected ? '解除保护' : '保护'}
        >
          {item.protected ? <ShieldOff className='size-3.5' /> : <Shield className='size-3.5' />}
        </button>
        <button
          type='button'
          aria-label={`删除本地副本 ${time}`}
          disabled={actionPending || !fillable}
          className='grid size-6 place-items-center rounded border border-video-border bg-video-surface-raised text-video-chrome-foreground/70 hover:text-red-400 disabled:opacity-30'
          title='删除本地副本'
        >
          <Trash2 className='size-3.5' />
        </button>
      </div>
    </div>
  )
}

// Retention is the one setting that deletes footage without anyone present, so
// the panel states the current occupancy and what the period would keep before
// the operator turns it on. The Collapsible wrapper around this panel renders
// the heading and the toggle; everything here is the body the trigger reveals.
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
    <div className='space-y-2'>
      <p className='text-xs text-video-chrome-foreground/70'>
        {enabled
          ? `超过 ${site.recording_retention_days} 天的录像会自动删除本地副本；已保护的录像始终跳过。`
          : '当前不自动删除任何录像。设置天数后，超过该期限的录像会自动删除本地副本（已保护的除外）。'}
      </p>
      {stats && (
        <p className='text-[11px] text-video-chrome-foreground/50'>
          当前筛选范围：{stats.segments} 段 · {bytes(stats.logical_bytes)} 可播放 ·
          已删除释放 {bytes(stats.deleted_bytes)}
        </p>
      )}
      <div className='flex items-end gap-2'>
        <SelectField
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
      <Notices {...action} />
    </div>
  )
}
