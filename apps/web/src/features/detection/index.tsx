import { type ReactNode, useMemo, useState } from 'react'
import { configuredChannels } from '@/features/foundation/channels'
import { useQueryClient } from '@tanstack/react-query'
import { apiRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { SlidersHorizontal } from 'lucide-react'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { LoadingButton } from '@/features/foundation/loading-button'
import { StatusBadge, type StatusTone } from '@/features/foundation/status-badge'
import { Empty, Notices, PageTitle, QueryState } from '@/features/foundation/ui'
import { cn } from '@/lib/utils'
import { DetectionConfigBar } from './config-bar'
import { SENSITIVITY_TEXT, STREAM_TEXT, configSummary } from './draft'
import { ZoneWorkbench } from './zones'

type Config = Schema<'DetectionConfig'>
type Runtime = Schema<'DetectionRuntime'>
type Channel = Schema<'Channel'>
type Summary = Schema<'ChannelSummary'>

const ERROR_CODES: Record<string, string> = {
  detection_label_unsupported: '模型不支持所选目标类别',
  detection_publish_failed: '发布失败，已保留原配置',
}

const errorText = (error: unknown) => apiErrorText(error, ERROR_CODES)

const HELP_TEXT =
  '检测从媒体服务器内部取流，只做检测与抓拍，不接管录像。' +
  '检测源指向通道当前取流的会话：重新取流、换源或服务重启后，系统会自动重新发布到检测服务，无需手动操作；' +
  '只有修改设置后才需要手动发布，未发布前检测服务保持原样。' +
  '发布失败时自动保留上一份配置。区域坐标按比例存储，与摄像头分辨率无关。'

// Row health, derived from the same summary data the channels page uses. A
// channel with no camera cannot run detection, so its row is inert.
function rowHealth(channel: Summary) {
  if (!channel.source_ip && !channel.current_revision_id) return 'unconfigured'
  if (!channel.enabled) return 'disabled'
  if (channel.main.state === 'healthy') return 'online'
  return 'offline'
}

/**
 * One metric in the page's status grid.
 *
 * The five facts differ in kind — a liveness verdict, two versions, a latency, a
 * hardware profile, a publish outcome — but they are read together ("is
 * detection working, and with what"), so they share one row of equal cards
 * rather than a paragraph of label/value pairs. The label carries the weight
 * here: the values are read once, the labels orient every glance.
 */
function StatCard({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className='flex min-h-24 flex-col rounded-lg border border-border bg-muted/40 p-4'>
      <p className='text-xs leading-5 text-muted-foreground'>{label}</p>
      <div className='mt-1 min-w-0 flex-1 text-sm font-medium leading-5 text-foreground'>
        {children}
      </div>
    </div>
  )
}

// PCI vendor ids are what the report carries; an operator recognises the name.
const VENDORS: Record<string, string> = {
  '8086': 'Intel',
  '10de': 'NVIDIA',
  '1a03': 'ASPEED',
}

/**
 * The accelerators and the engine that uses them, as one card.
 *
 * These were two cards ("版本 / 检测器" and "检测硬件") and neither answered the
 * question on its own: the version is meaningless without knowing which device
 * it drives, and the device path is meaningless without knowing which detector
 * build opened it. They are read together, so they are one card.
 *
 * The sub-line is one line on a wide screen and wraps rather than truncating on
 * a narrow one. These are identifiers matched character by character against
 * the detector's own config, so an ellipsis would cut off exactly the part that
 * distinguishes one GPU from another.
 */
function HardwareEngineCard({
  report,
  engine,
}: {
  report: Schema<'HardwareReport'> | undefined
  engine: Runtime['runtime'] | undefined
}) {
  const node = report?.proposal.node
  const vendor = report?.inventory.devices.find(
    (device) => device.id === report?.proposal.id || device.node === node
  )?.vendor
  return (
    <StatCard label='检测硬件与引擎'>
      <span className='flex min-w-0 flex-wrap items-center gap-1.5'>
        <span className='truncate font-mono text-sm'>{node || 'CPU'}</span>
        {vendor && (
          <Badge
            variant='outline'
            className='border-solid border-border px-1.5 py-0 text-[11px] font-normal text-muted-foreground'
          >
            {VENDORS[vendor] || vendor}
          </Badge>
        )}
        {engine?.version && (
          <Badge
            variant='outline'
            className='border-solid border-border px-1.5 py-0 text-[11px] font-normal text-muted-foreground'
          >
            v{engine.version}
          </Badge>
        )}
        {report && !report.validated && (
          <StatusBadge tone='warning'>
            自检未通过{report.error_code ? `：${report.error_code}` : ''}
          </StatusBadge>
        )}
      </span>
      {report && (
        <span className='mt-1 flex flex-wrap gap-x-3 font-mono text-xs font-normal leading-4 text-muted-foreground'>
          <span className='whitespace-nowrap'>解码 {report.proposal.decode_id || 'cpu'}</span>
          <span className='whitespace-nowrap'>推理 {report.proposal.inference_id || 'cpu'}</span>
        </span>
      )}
    </StatCard>
  )
}

/**
 * The liveness badge for a service that is answering.
 *
 * A pulsing dot rather than a static pill: "运行中" is a claim about right now,
 * and a badge that looks the same whether the poll just succeeded or never has
 * makes the operator read the colour instead of the timestamp. The pulse stops
 * under prefers-reduced-motion.
 */
function LiveBadge() {
  return (
    <span className='inline-flex items-center gap-1.5 rounded-full border border-emerald-600/30 bg-emerald-500/10 px-2 py-0.5 text-xs text-emerald-700 dark:border-emerald-500/40 dark:bg-emerald-500/15 dark:text-emerald-400'>
      <span className='relative flex size-1.5'>
        <span className='absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-500 opacity-60 motion-reduce:hidden' />
        <span className='relative inline-flex size-1.5 rounded-full bg-emerald-500' />
      </span>
      运行中
    </span>
  )
}

type RowFilter = 'all' | 'enabled' | 'unconfigured'

const HEALTH_BADGES: Record<RowHealth, { tone: StatusTone; text: string }> = {
  online: { tone: 'success', text: '在线' },
  offline: { tone: 'warning', text: '离线' },
  disabled: { tone: 'neutral', text: '通道已停用' },
  unconfigured: { tone: 'neutral', text: '未配置源' },
  unknown: { tone: 'neutral', text: '未知' },
}
type RowHealth = 'online' | 'offline' | 'disabled' | 'unconfigured' | 'unknown'

/**
 * M3 batch 2 (AI-01/AI-07): detection settings as one bar plus a read-only table.
 *
 * All five settings live in the bar and apply to whatever is selected. The table
 * answers "what did each channel end up with" — it has no controls of its own, so
 * there is exactly one place a setting can be changed. Publishing stays a
 * separate, explicit action: edits only save, and the sticky bar reloads Frigate.
 */
export function Detection() {
  const client = useQueryClient()
  const channels = useAPI<PageData<Channel>>('/api/v1/channels?limit=100')
  const summaries = useAPI<{ items: Summary[] }>('/api/v1/channels/summary')
  const configs = useAPI<{ items: Config[] }>('/api/v1/detection/configs')
  const runtime = useAPI<Runtime>('/api/v1/detection/runtime')
  // The detector's hardware profile: which device it runs on and how long a
  // sample decode and inference took. It lived on the site settings page, which
  // is where a site is configured — but these numbers are read when detection
  // feels slow, and that question is asked here.
  const hardware = useAPI<Schema<'HardwareReport'>>('/api/v1/settings/hardware', true, 30_000)
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  const [filter, setFilter] = useState<RowFilter>('all')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [openWorkbench, setOpenWorkbench] = useState<string>('')

  const summaryOf = useMemo(
    () => new Map((summaries.data?.items || []).map((item) => [item.channel_id, item])),
    [summaries.data]
  )
  const configOf = useMemo(
    () => new Map((configs.data?.items || []).map((config) => [config.channel_id ?? '', config])),
    [configs.data]
  )
  const permitted = configuredChannels(
    (channels.data?.items || []).filter((channel) => channel.permissions.includes('configure'))
  )
  // Only a channel the server will accept can be selected: applying to an
  // unconfigured channel wastes a round trip and reports a failure the operator
  // cannot act on from here.
  const actionable = permitted.filter((channel) => {
    const summary = summaryOf.get(channel.id ?? '')
    return summary ? rowHealth(summary) !== 'unconfigured' : false
  })
  const chosen = permitted.filter((channel) => selected.has(channel.id ?? ''))
  const counts = {
    all: permitted.length,
    enabled: permitted.filter((channel) => configOf.get(channel.id ?? '')?.enabled).length,
    unconfigured: permitted.length - actionable.length,
  }
  const visibleRows = permitted.filter((channel) => {
    if (filter === 'enabled') return !!configOf.get(channel.id ?? '')?.enabled
    if (filter === 'unconfigured') return !actionable.includes(channel)
    return true
  })
  const pendingPublish = runtime.data?.revision?.pending ?? false
  // The header checkbox speaks for the channels the server will accept. It is
  // "select all that can be changed", not "select all rows": ticking a row the
  // server refuses would make Apply report a failure the operator cannot fix
  // from this page.
  const selectableCount = actionable.length
  const selectedActionableCount = actionable.filter((channel) =>
    selected.has(channel.id ?? '')
  ).length
  // A filtered table still selects across filters — the selection is the thing
  // Apply acts on, and losing it when the operator peeks at another tab would
  // silently shrink a batch they already assembled.
  const visibleSelectedCount = visibleRows.filter((channel) =>
    selected.has(channel.id ?? '')
  ).length

  async function run(action: () => Promise<void>, message: string) {
    setPending(true)
    setError('')
    setNotice('')
    try {
      await action()
      setNotice(message)
      await client.invalidateQueries()
    } catch (cause) {
      setError(errorText(cause))
    } finally {
      setPending(false)
    }
  }

  const publish = () =>
    void run(async () => {
      await apiRequest('/api/v1/detection/publish', { method: 'POST' })
    }, '检测配置已发布')

  function toggle(id: string, checked: boolean) {
    setSelected((previous) => {
      const next = new Set(previous)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  return (
    <div className='space-y-4 pb-20'>
      <PageTitle
        title='检测配置'
        description='在上方选择码流、帧率、灵敏度与目标，勾选通道后一次应用；下方表格只展示每路当前生效的配置。'
      >
        <span
          role='note'
          aria-label='检测配置使用说明'
          title={HELP_TEXT}
          className='grid size-5 cursor-help place-items-center rounded-full border border-border text-[10px] text-muted-foreground'
        >
          ?
        </span>
      </PageTitle>
      <QueryState
        pending={channels.isPending || configs.isPending}
        error={channels.error || configs.error}
        retry={() => void channels.refetch()}
      />
      <Notices notice={notice} error={error} />
      {runtime.data && (
        <section
          aria-label='检测状态'
          className='grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4'
        >
          <StatCard label='检测服务'>
            {runtime.data.runtime?.reachable ? (
              <LiveBadge />
            ) : (
              <StatusBadge tone='destructive'>不可达</StatusBadge>
            )}
            <span className='mt-1 block text-xs font-normal text-muted-foreground'>
              检测器 {runtime.data.runtime?.detector || '—'}
            </span>
          </StatCard>
          <StatCard label='推理耗时'>
            {runtime.data.runtime?.inference_ms ? (
              <span className='tabular-nums'>
                {runtime.data.runtime.inference_ms.toFixed(1)} ms
              </span>
            ) : (
              '—'
            )}
            {runtime.data.runtime?.overloaded ? (
              <StatusBadge tone='warning' className='ml-2'>
                过载
              </StatusBadge>
            ) : (
              <span className='mt-1 block text-xs font-normal text-muted-foreground'>
                单帧推理耗时
              </span>
            )}
          </StatCard>
          <HardwareEngineCard report={hardware.data} engine={runtime.data.runtime} />
          <StatCard label='最近发布'>
            {runtime.data.revision ? (
              <>
                <span className='flex flex-wrap items-center gap-1.5'>
                  <StatusBadge
                    tone={
                      runtime.data.revision.state === 'published' ? 'success' : 'destructive'
                    }
                  >
                    {runtime.data.revision.state === 'published' ? '成功' : '失败'}
                  </StatusBadge>
                  <span className='text-xs font-normal tabular-nums text-muted-foreground'>
                    {runtime.data.revision.camera_count} 路
                  </span>
                  {runtime.data.revision.pending && (
                    <StatusBadge tone='warning'>未发布</StatusBadge>
                  )}
                </span>
                <span className='block text-xs font-normal text-muted-foreground'>
                  {runtime.data.revision.pending ? '检测服务仍用上一份配置' : '与检测服务一致'}
                </span>
              </>
            ) : (
              <span className='text-muted-foreground'>尚未发布</span>
            )}
          </StatCard>
        </section>
      )}
      {!permitted.length && channels.data ? (
        <Empty>没有可配置检测的通道，请联系管理员分配通道配置权限。</Empty>
      ) : (
        <>
          <DetectionConfigBar
            chosen={chosen}
            chosenCount={chosen.length}
            total={permitted.length}
            onSelectAll={() =>
              setSelected(new Set(actionable.map((channel) => channel.id ?? '')))
            }
            onClear={() => setSelected(new Set())}
            configOf={configOf}
          />
          <div className='flex flex-wrap items-center justify-between gap-3'>
            <div
              role='group'
              aria-label='通道筛选'
              className='inline-flex items-center gap-1 rounded-lg border border-border bg-muted/30 p-1'
            >
              {(
                [
                  { value: 'all', label: '全部' },
                  { value: 'enabled', label: '已启用检测' },
                  { value: 'unconfigured', label: '未配置源' },
                ] as const
              ).map(({ value, label }) => {
                const active = filter === value
                return (
                  <button
                    key={value}
                    type='button'
                    aria-pressed={active}
                    onClick={() => setFilter(value)}
                    className={cn(
                      'inline-flex h-7 items-center gap-1.5 rounded-md px-2.5 text-xs transition-colors',
                      active
                        ? 'bg-background font-medium text-foreground shadow-sm ring-1 ring-border'
                        : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground'
                    )}
                  >
                    {label}
                    <span
                      className={cn(
                        'tabular-nums',
                        active ? 'text-foreground/60' : 'text-muted-foreground/70'
                      )}
                    >
                      {counts[value]}
                    </span>
                  </button>
                )
              })}
            </div>
            <p className='text-xs text-muted-foreground'>
              {visibleSelectedCount > 0
                ? `当前筛选内已选 ${visibleSelectedCount} 路`
                : '勾选通道后可在上方批量修改'}
            </p>
          </div>
          <div className='overflow-hidden rounded-lg border border-border bg-card'>
            <Table>
              <TableHeader className='bg-muted/80'>
                <TableRow className='border-b border-border hover:bg-transparent'>
                  <TableHead className='w-10 py-3 pl-4 align-middle'>
                    <Checkbox
                      className='translate-y-0 border-border hover:border-foreground/40'
                      aria-label='全选可配置通道'
                      checked={
                        selectableCount > 0 && selectedActionableCount === selectableCount
                          ? true
                          : selectedActionableCount > 0
                            ? 'indeterminate'
                            : false
                      }
                      disabled={selectableCount === 0}
                      onCheckedChange={(state) =>
                        setSelected(
                          state === true
                            ? new Set(actionable.map((channel) => channel.id ?? ''))
                            : new Set()
                        )
                      }
                    />
                  </TableHead>
                  <TableHead className='w-28 py-3'>通道</TableHead>
                  <TableHead className='w-24 py-3'>状态</TableHead>
                  <TableHead className='w-24 py-3'>检测</TableHead>
                  <TableHead className='w-24 py-3'>码流</TableHead>
                  <TableHead className='w-20 py-3'>帧率</TableHead>
                  <TableHead className='w-24 py-3'>灵敏度</TableHead>
                  <TableHead className='py-3'>目标</TableHead>
                  <TableHead className='w-36 py-3 pr-4 text-right'>区域与规则</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visibleRows.map((channel) => {
                  const id = channel.id ?? ''
                  const config = configOf.get(id)
                  const summary = summaryOf.get(id)
                  const health: RowHealth = summary ? rowHealth(summary) : 'unknown'
                  const badge = HEALTH_BADGES[health]
                  const inert = health === 'unconfigured' || health === 'unknown'
                  const enabled = config?.enabled ?? false
                  const labels = config?.labels ?? []
                  return (
                    <ChannelRow
                      key={id}
                      channel={channel}
                      badge={badge}
                      inert={inert}
                      enabled={enabled}
                      stream={config?.stream ?? 'sub'}
                      fps={config?.fps ?? 5}
                      sensitivity={config?.sensitivity ?? 'balanced'}
                      labels={labels}
                      selected={selected.has(id)}
                      workbenchOpen={openWorkbench === id}
                      onSelect={(checked) => toggle(id, checked)}
                      onToggleWorkbench={() =>
                        setOpenWorkbench(openWorkbench === id ? '' : id)
                      }
                      label={`CH${String(channel.channel_no ?? 0).padStart(2, '0')} ${channel.channel_name}`}
                    />
                  )
                })}
              </TableBody>
            </Table>
            {visibleRows.length === 0 && (
              <div className='p-8 text-center text-sm text-muted-foreground'>
                没有匹配的通道
              </div>
            )}
          </div>
        </>
      )}
      {pendingPublish && permitted.length > 0 && (
        <div
          data-testid='publish-bar'
          className='sticky bottom-0 z-20 -mx-4 flex flex-wrap items-center justify-between gap-3 rounded-t-lg border-t border-amber-600/40 bg-card/95 px-4 py-2.5 backdrop-blur'
        >
          <span className='text-sm text-amber-600 dark:text-amber-400'>
            有设置尚未发布，检测服务仍在使用上一份配置
          </span>
          <LoadingButton pending={pending} pendingText='发布中…' onClick={publish}>
            发布检测配置
          </LoadingButton>
        </div>
      )}
    </div>
  )
}

/**
 * One channel as the table shows it: state and current settings, no controls.
 *
 * The zones/rules toggle stays because it opens that channel's workbench — it is
 * a second editor for geometry, not a sixth copy of the settings above, and the
 * bar cannot draw on a frame it does not have.
 */
function ChannelRow({
  channel,
  badge,
  inert,
  enabled,
  stream,
  fps,
  sensitivity,
  labels,
  selected,
  workbenchOpen,
  onSelect,
  onToggleWorkbench,
  label,
}: {
  channel: Channel
  badge: { tone: StatusTone; text: string }
  inert: boolean
  enabled: boolean
  stream: string
  fps: number
  sensitivity: string
  labels: string[]
  selected: boolean
  workbenchOpen: boolean
  onSelect: (checked: boolean) => void
  onToggleWorkbench: () => void
  label: string
}) {
  const interactive = !inert
  return (
    <>
      <TableRow
        data-testid='detection-row'
        data-selected={selected || undefined}
        className={cn(
          interactionClass(interactive, selected),
          inert && 'opacity-60'
        )}
        onClick={() => interactive && onSelect(!selected)}
      >
        <TableCell className='py-3 pl-4 align-middle'>
          {/* The checkbox mirrors the row: clicking either is the same act. It
              stays disabled on a row the server would refuse, so a selection can
              never contain something Apply is guaranteed to reject.

              The table cell defaults to align-top (a plan row may expand an
              editor below its controls); this column is a single 16px box, so
              top alignment would leave it floating above the row's baseline. */}
          <Checkbox
            aria-label={`选择 ${label}`}
            checked={selected}
            disabled={!interactive}
            onCheckedChange={(state) => onSelect(state === true)}
            onClick={(event) => event.stopPropagation()}
            className='translate-y-0 border-border hover:border-foreground/40 data-[state=checked]:border-primary'
          />
        </TableCell>
        <TableCell className='py-3 font-medium'>{label}</TableCell>
        <TableCell className='py-3'>
          <HealthBadge badge={badge} />
        </TableCell>
        <TableCell className='py-3'>
          <DetectionFlag enabled={enabled} inert={inert} />
        </TableCell>
        <TableCell className='py-3 text-muted-foreground'>
          {STREAM_TEXT[stream] ?? stream}
        </TableCell>
        <TableCell className='py-3 tabular-nums text-muted-foreground'>{fps} fps</TableCell>
        <TableCell className='py-3 text-muted-foreground'>
          {SENSITIVITY_TEXT[sensitivity as keyof typeof SENSITIVITY_TEXT] ?? sensitivity}
        </TableCell>
        <TableCell className='py-3 text-muted-foreground'>
          {configSummary(labels)}
          {inert && (
            <span className='ml-2 text-xs text-muted-foreground/70'>先在通道管理接入摄像头</span>
          )}
        </TableCell>
        <TableCell
          className='py-3 pr-4 text-right'
          onClick={(event) => event.stopPropagation()}
        >
          <ZoneRuleToggle
            open={workbenchOpen}
            disabled={!interactive}
            label={label}
            channelID={channel.id ?? ''}
            onToggle={onToggleWorkbench}
          />
        </TableCell>
      </TableRow>
      {workbenchOpen && (
        <TableRow>
          <TableCell colSpan={9} className='bg-muted/20 p-0'>
            <ZoneWorkbench channelID={channel.id ?? ''} detectionEnabled={enabled} />
          </TableCell>
        </TableRow>
      )}
    </>
  )
}

/**
 * Health keeps a coloured pill only when something is wrong.
 *
 * A healthy channel is the normal case: painting it green on every row spends
 * the operator's attention on the thing that needs none, and then a genuine
 * amber row has to compete with eight green ones. So "online" is a quiet dot,
 * and the states that need action keep the badge.
 */
function HealthBadge({ badge }: { badge: { tone: StatusTone; text: string } }) {
  if (badge.tone === 'success') {
    return (
      <span className='inline-flex items-center gap-1.5 text-xs text-muted-foreground'>
        <span aria-hidden className='size-1.5 rounded-full bg-emerald-500' />
        {badge.text}
      </span>
    )
  }
  return <StatusBadge tone={badge.tone}>{badge.text}</StatusBadge>
}

/**
 * The detection flag, drawn as a state rather than as a second status pill.
 *
 * It repeats what the bar's switch does to each channel, so it reads as on or
 * off in quiet text: the healthy, expected value must not look like an alert.
 */
function DetectionFlag({ enabled, inert }: { enabled: boolean; inert: boolean }) {
  if (inert) return <span className='text-xs text-muted-foreground/70'>—</span>
  // A badge, not a dot: the detection flag is a configuration value this page
  // writes, and it should read as one. The online dot stays a dot because it is
  // a measurement that changes on its own — the two must not look like the same
  // kind of fact.
  return (
    <Badge
      variant='outline'
      className={cn(
        'border-solid px-2 py-0 text-xs font-normal',
        enabled
          ? 'border-emerald-500/20 bg-emerald-500/10 text-emerald-700 dark:border-emerald-500/25 dark:text-emerald-400'
          : 'border-border bg-muted text-muted-foreground'
      )}
    >
      {enabled ? '已启用' : '未启用'}
    </Badge>
  )
}

function interactionClass(interactive: boolean, selected: boolean) {
  if (!interactive) return 'cursor-not-allowed'
  return cn(
    'cursor-pointer transition-colors',
    selected
      ? 'bg-primary/[0.12] hover:bg-primary/[0.16]'
      : 'hover:bg-muted/40'
  )
}

/**
 * The zones/rules count lives on the toggle so fences are visible unexpanded.
 *
 * It stays in the table because it opens that channel's workbench: geometry is a
 * second editor, not a sixth copy of the settings the bar owns.
 */
function ZoneRuleToggle({
  open,
  disabled,
  label,
  channelID,
  onToggle,
}: {
  open: boolean
  disabled: boolean
  label: string
  channelID: string
  onToggle: () => void
}) {
  const zones = useAPI<Schema<'DetectionZonePage'>>(
    `/api/v1/detection/zones?channel_id=${channelID}`,
    !disabled
  )
  const rules = useAPI<Schema<'DetectionRulePage'>>(
    `/api/v1/detection/rules?channel_id=${channelID}`,
    !disabled
  )
  const zoneCount = zones.data?.items?.length
  const ruleCount = rules.data?.items?.length
  // The counts are the useful part: "configured or not" is answerable without
  // opening anything, which is the whole reason the toggle stays in the row.
  const counts =
    zoneCount != null && ruleCount != null ? `${zoneCount}/${ruleCount}` : ''
  const text = open ? '收起' : '配置规则'
  return (
    <Button
      type='button'
      size='sm'
      variant='ghost'
      className='h-8 gap-1.5 px-2.5 text-xs font-normal text-muted-foreground hover:bg-accent hover:text-foreground'
      aria-expanded={open}
      aria-label={`${label} 区域与规则${counts ? ` (${counts})` : ''}`}
      title={`${zoneCount ?? 0} 区域 · ${ruleCount ?? 0} 规则`}
      disabled={disabled}
      onClick={onToggle}
    >
      <SlidersHorizontal aria-hidden className='size-3.5' />
      {text}
      {counts && <span className='tabular-nums opacity-70'>({counts})</span>}
    </Button>
  )
}
