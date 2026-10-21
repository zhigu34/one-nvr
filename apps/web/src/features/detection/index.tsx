import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { LoadingButton } from '@/features/foundation/loading-button'
import { StatusBadge } from '@/features/foundation/status-badge'
import { Empty, Notices, QueryState, SelectField } from '@/features/foundation/ui'
import { cn } from '@/lib/utils'
import { ZoneWorkbench } from './zones'

type Config = Schema<'DetectionConfig'>
type Runtime = Schema<'DetectionRuntime'>
type Channel = Schema<'Channel'>
type Summary = Schema<'ChannelSummary'>

const LABEL_TEXT: Record<string, string> = {
  person: '人', bicycle: '自行车', car: '轿车', motorcycle: '摩托车', bus: '巴士',
  truck: '卡车', cat: '猫', dog: '狗', bird: '鸟', horse: '马', sheep: '羊', cow: '牛', bear: '熊',
}

// The detector's vocabulary is broad; a row shows the common six and keeps the
// rest one click away instead of spraying the whole model across every row.
const COMMON_LABELS = ['person', 'car', 'bicycle', 'motorcycle', 'truck', 'bus']
const MORE_LABELS = ['cat', 'dog', 'bird', 'horse', 'sheep', 'cow', 'bear']

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
// channel with no camera cannot run detection, so its whole row is inert.
function rowHealth(channel: Summary) {
  if (!channel.source_ip && !channel.current_revision_id) return 'unconfigured'
  if (!channel.enabled) return 'disabled'
  if (channel.main.state === 'healthy') return 'online'
  return 'offline'
}

const HEALTH_BADGES: Record<RowHealth, { tone: 'success' | 'warning' | 'neutral' | 'destructive'; text: string }> = {
  online: { tone: 'success', text: '在线' },
  offline: { tone: 'warning', text: '离线' },
  disabled: { tone: 'neutral', text: '通道已停用' },
  unconfigured: { tone: 'neutral', text: '未配置源' },
  unknown: { tone: 'neutral', text: '未知' },
}

type RowFilter = 'all' | 'enabled' | 'dirty'
type RowHealth = 'online' | 'offline' | 'disabled' | 'unconfigured' | 'unknown'

/**
 * M3 batch 2 (AI-01/AI-07): per-channel detection settings and the capability
 * card. Publication is an explicit action: editing a row only saves it, and
 * the sticky bar renders and reloads Frigate's config. Recording is never
 * touched by any of this.
 */
export function Detection() {
  const client = useQueryClient()
  const channels = useAPI<PageData<Channel>>('/api/v1/channels?limit=100')
  const summaries = useAPI<{ items: Summary[] }>('/api/v1/channels/summary')
  const configs = useAPI<{ items: Config[] }>('/api/v1/detection/configs')
  const runtime = useAPI<Runtime>('/api/v1/detection/runtime')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  // Rows the operator edited but has not saved yet; the sticky publish bar
  // counts them so "保存再发布" never happens out of order.
  const [dirty, setDirty] = useState<Set<string>>(new Set())
  const [filter, setFilter] = useState<RowFilter>('all')
  const summaryOf = new Map(
    (summaries.data?.items || []).map((item) => [item.channel_id, item])
  )
  const permitted = (channels.data?.items || []).filter((channel) =>
    channel.permissions.includes('configure')
  )
  const configOf = new Map((configs.data?.items || []).map((config) => [config.channel_id ?? '', config]))
  const rows = permitted.map((channel) => {
    const summary = summaryOf.get(channel.id ?? '')
    const config = configOf.get(channel.id ?? '')
    const health: RowHealth = summary ? rowHealth(summary) : 'unknown'
    return {
      channel,
      config,
      health,
      enabled: config?.enabled ?? false,
    }
  })
  const counts = {
    all: rows.length,
    enabled: rows.filter((row) => row.enabled).length,
    dirty: dirty.size,
  }
  const visibleRows = rows.filter((row) => {
    if (filter === 'enabled') return row.enabled
    if (filter === 'dirty') return dirty.has(row.channel.id ?? '')
    return true
  })
  const pendingPublish = runtime.data?.revision?.pending ?? false
  const hasUnsaved = counts.dirty > 0

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
      setDirty(new Set())
    }, '检测配置已发布')

  return (
    <div className='space-y-4'>
      <div>
        <div className='flex items-center gap-2'>
          <h1 className='text-2xl font-semibold tracking-tight'>检测配置</h1>
          <span
            role='note'
            aria-label='检测配置使用说明'
            title={HELP_TEXT}
            className='grid size-5 cursor-help place-items-center rounded-full border border-border text-[10px] text-muted-foreground'
          >
            ?
          </span>
        </div>
        <p className='mt-1 text-sm text-muted-foreground'>
          按通道启停智能检测并选择码流、帧率与目标类别；发布后检测服务重载一次，失败时保留上一份配置。
        </p>
      </div>
      <QueryState
        pending={channels.isPending || configs.isPending}
        error={channels.error || configs.error}
        retry={() => void channels.refetch()}
      />
      <Notices notice={notice} error={error} />
      {runtime.data && (
          <div className='grid gap-2 rounded-xl border bg-card p-4 text-sm sm:grid-cols-2 lg:grid-cols-4'>
            <div>
              <p className='text-xs text-muted-foreground'>检测服务</p>
              <p>
                <StatusBadge
                  tone={runtime.data.runtime?.reachable ? 'success' : 'destructive'}
                >
                  {runtime.data.runtime?.reachable ? '运行中' : '不可达'}
                </StatusBadge>
              </p>
            </div>
          <div>
            <p className='text-xs text-muted-foreground'>版本 / 检测器</p>
            <p>{runtime.data.runtime?.version || '—'} · {runtime.data.runtime?.detector || '—'}</p>
          </div>
          <div>
            <p className='text-xs text-muted-foreground'>推理耗时</p>
            <p>
              {runtime.data.runtime?.inference_ms ? `${runtime.data.runtime.inference_ms.toFixed(1)} ms` : '—'}
              {runtime.data.runtime?.overloaded ? ' · 过载' : ''}
            </p>
          </div>
          <div>
            <p className='text-xs text-muted-foreground'>最近发布</p>
            <p>
              {runtime.data.revision
                ? `${runtime.data.revision.state === 'published' ? '成功' : '失败'} · ${runtime.data.revision.camera_count} 路`
                : '尚未发布'}
            </p>
            {runtime.data.revision && (
              <p
                className={
                  runtime.data.revision.pending
                    ? 'mt-1 text-xs text-amber-600'
                    : 'mt-1 text-xs text-muted-foreground'
                }
              >
                {runtime.data.revision.pending
                  ? '有设置尚未发布'
                  : '与检测服务一致'}
              </p>
            )}
          </div>
        </div>
      )}
      {(!permitted.length && channels.data) ? (
        <Empty>没有可配置检测的通道，请联系管理员分配通道配置权限。</Empty>
      ) : (
        <>
          <div
            role='group'
            aria-label='通道筛选'
            className='flex flex-wrap items-center gap-1'
          >
            {(
              [
                { value: 'all', label: '全部' },
                { value: 'enabled', label: '已启用检测' },
                { value: 'dirty', label: '未保存修改' },
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
                    'inline-flex h-7 items-center gap-1 rounded-full border px-2.5 text-xs transition-colors',
                    active
                      ? 'border-primary bg-primary/10 font-medium text-primary'
                      : 'border-border text-muted-foreground hover:bg-accent hover:text-foreground'
                  )}
                >
                  {label}
                  <span className={cn('tabular-nums', active ? '' : 'opacity-70')}>
                    {counts[value]}
                  </span>
                </button>
              )
            })}
          </div>
          {/* pb-20 keeps the last row clear of the sticky publish bar when
              scrolled to the bottom. */}
          <div className='space-y-2 pb-20'>
            {visibleRows.map((row) => (
              <DetectionRow
                key={row.channel.id}
                channel={row.channel}
                config={row.config}
                health={row.health}
                dirty={dirty.has(row.channel.id ?? '')}
                onDirty={(value) =>
                  setDirty((current) => {
                    const next = new Set(current)
                    if (value) next.add(row.channel.id ?? '')
                    else next.delete(row.channel.id ?? '')
                    return next
                  })
                }
                pending={pending}
                onSave={(next) =>
                  void run(async () => {
                    await apiRequest(
                      `/api/v1/detection/configs/${row.channel.id}`,
                      jsonRequest('PUT', { ...next, version: row.config?.version ?? 0 })
                    )
                    onDirty(row.channel.id ?? '', false)
                  }, '检测配置已保存，发布后生效')
                }
              />
            ))}
          </div>
        </>
      )}
      {(pendingPublish || hasUnsaved) && permitted.length > 0 && (
        <div
          data-testid='publish-bar'
          className='sticky bottom-0 z-20 -mx-4 flex flex-wrap items-center justify-between gap-3 border-t border-amber-600/40 bg-card/95 px-4 py-2.5 backdrop-blur'
        >
          <span className='text-sm text-amber-600'>
            {hasUnsaved
              ? `${counts.dirty} 路有未保存的修改，发布前请先保存`
              : '有设置尚未发布，检测服务仍在使用上一份配置'}
          </span>
          <LoadingButton
            pending={pending}
            disabled={hasUnsaved}
            pendingText='发布中…'
            onClick={publish}
          >
            发布检测配置
          </LoadingButton>
        </div>
      )}
    </div>
  )

  function onDirty(id: string, value: boolean) {
    setDirty((current) => {
      const next = new Set(current)
      if (value) next.add(id)
      else next.delete(id)
      return next
    })
  }
}

function DetectionRow({
  channel,
  config,
  health,
  dirty,
  onDirty,
  pending,
  onSave,
}: {
  channel: Channel
  config?: Config
  health: RowHealth
  dirty: boolean
  onDirty: (value: boolean) => void
  pending: boolean
  onSave: (next: {
    enabled: boolean
    stream: string
    fps: number
    labels: string[]
    sensitivity: 'sensitive' | 'balanced' | 'strict'
  }) => void
}) {
  // The row must follow the stored configuration, not a snapshot taken at
  // mount: the page renders before the config query resolves, and React reuses
  // these component instances once it does, so state seeded from an initially
  // undefined config would silently show defaults for every channel.
  const [draft, setDraft] = useState<{
    enabled: boolean
    stream: string
    fps: number
    labels: string[]
    sensitivity: 'sensitive' | 'balanced' | 'strict'
  } | null>(null)
  const [workbenchOpen, setWorkbenchOpen] = useState(false)
  const enabled = draft?.enabled ?? config?.enabled ?? false
  const stream = draft?.stream ?? config?.stream ?? 'sub'
  const fps = draft?.fps ?? config?.fps ?? 5
  // Sensitivity decides how readily a detection is called; it is the one
  // setting an operator changes when the detector is too eager or too shy.
  const sensitivity = draft?.sensitivity ?? config?.sensitivity ?? 'balanced'
  const selected = draft?.labels ?? config?.labels ?? ['person']
  const label = `CH${String(channel.channel_no ?? 0).padStart(2, '0')} ${channel.channel_name}`
  const extra = selected.filter((value) => !COMMON_LABELS.includes(value))
  const summaryText =
    selected.length === 0
      ? '未选择'
      : selected.length <= 3
        ? selected.map((value) => LABEL_TEXT[value] || value).join('、')
        : `${selected.length} 类`
  // A channel without a camera cannot stream into the detector: nothing on
  // this row is actionable and the workbench has no frame to draw on.
  const inert = health === 'unconfigured' || health === 'unknown'
  const badge = health === 'unknown' ? null : HEALTH_BADGES[health]
  const savedEnabled = config?.enabled ?? false
  const savedStream = config?.stream ?? 'sub'
  const savedFps = config?.fps ?? 5
  const savedLabels = config?.labels ?? ['person']
  const savedSensitivity = config?.sensitivity ?? 'balanced'
  const unchanged =
    !draft ||
    (enabled === savedEnabled &&
      stream === savedStream &&
      fps === savedFps &&
      sensitivity === savedSensitivity &&
      selected.length === savedLabels.length &&
      selected.every((value) => savedLabels.includes(value)))

  function mutate(
    patch: Partial<{
      enabled: boolean
      stream: string
      fps: number
      labels: string[]
      sensitivity: 'sensitive' | 'balanced' | 'strict'
    }>
  ) {
    setDraft({ enabled, stream, fps, labels: selected, sensitivity, ...patch })
    onDirty(true)
  }

  function toggle(value: string, next: boolean) {
    mutate({
      labels: next
        ? [...new Set([...selected, value])]
        : selected.filter((item) => item !== value),
    })
  }

  return (
    <div
      className={cn(
        'space-y-2 rounded-xl border bg-card p-3 text-sm',
        // Enabled rows get a faint emerald edge so the fence glance reads at a
        // distance; inert rows dim out entirely.
        enabled && !inert && 'border-l-2 border-l-emerald-500/60',
        inert && 'opacity-60'
      )}
    >
      <div className='flex flex-wrap items-center gap-x-4 gap-y-2' data-testid='detection-row'>
        <span
          className={cn(
            'w-40 shrink-0 truncate font-medium',
            inert && 'text-zinc-500'
          )}
        >
          {label}
        </span>
        {/* Fixed-width slot: the health labels differ by three characters, and
            without a reserved box every control to the right shifts per row. */}
        <span className='inline-flex w-24 shrink-0 justify-start'>
          {badge ? (
            <StatusBadge tone={badge.tone}>{badge.text}</StatusBadge>
          ) : null}
        </span>
        <label className='flex items-center gap-2'>
          <Switch
            checked={enabled}
            disabled={pending || inert}
            onCheckedChange={(next) => mutate({ enabled: next })}
            aria-label={`${label} 启用检测`}
          />
          启用检测
        </label>
        {/* Labels live in the row header via ariaLabel only: repeating the
            channel name on every control is what pushed these to two lines. */}
        <div className='flex items-center gap-2'>
          <span className='text-xs text-muted-foreground'>码流</span>
          <SelectField
            ariaLabel={`${label} 检测码流`}
            value={stream}
            disabled={inert}
            onValueChange={(next) => setStream(next)}
            options={[
              { value: 'sub', label: '子码流' },
              { value: 'main', label: '主码流' },
            ]}
            className='w-24'
          />
        </div>
        <div className='flex items-center gap-2'>
          <span className='text-xs text-muted-foreground'>帧率</span>
          <SelectField
            ariaLabel={`${label} 检测帧率`}
            value={String(fps)}
            disabled={inert}
            onValueChange={(next) => setFps(next)}
            options={[2, 5, 10, 15].map((value) => ({ value: String(value), label: `${value} fps` }))}
            className='w-20'
          />
        </div>
        <div className='flex items-center gap-2'>
          <span className='text-xs text-muted-foreground'>灵敏度</span>
          <SelectField
            ariaLabel={`${label} 检测灵敏度`}
            value={sensitivity}
            disabled={inert}
            onValueChange={(next) =>
              mutate({ sensitivity: (next ?? 'balanced') as typeof sensitivity })
            }
            options={[
              { value: 'sensitive', label: '灵敏' },
              { value: 'balanced', label: '标准' },
              { value: 'strict', label: '严格' },
            ]}
            className='w-20'
          />
        </div>
        <div className='flex items-center gap-2'>
          <span className='text-xs text-muted-foreground'>目标</span>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant='outline'
                size='sm'
                disabled={inert}
                aria-label={`${label} 目标类别`}
              >
                {summaryText}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align='start' className='w-44'>
              <DropdownMenuLabel>常用目标</DropdownMenuLabel>
              {COMMON_LABELS.map((value) => (
                <DropdownMenuCheckboxItem
                  key={value}
                  checked={selected.includes(value)}
                  onSelect={(event) => event.preventDefault()}
                  onCheckedChange={(next) => toggle(value, next === true)}
                >
                  {LABEL_TEXT[value] || value}
                </DropdownMenuCheckboxItem>
              ))}
              <DropdownMenuLabel>其他</DropdownMenuLabel>
              {MORE_LABELS.map((value) => (
                <DropdownMenuCheckboxItem
                  key={value}
                  checked={selected.includes(value)}
                  onSelect={(event) => event.preventDefault()}
                  onCheckedChange={(next) => toggle(value, next === true)}
                >
                  {LABEL_TEXT[value] || value}
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
          {extra.length > 0 && (
            <span className='text-xs text-muted-foreground'>+{extra.length} 类</span>
          )}
        </div>
        {inert && (
          <span
            className='text-xs text-muted-foreground'
            title='请先在通道管理中接入摄像头'
          >
            请先在通道管理中接入摄像头
          </span>
        )}
        <div className='ml-auto flex items-center gap-2'>
          {/* Unchanged rows have nothing to save: the dimmed button states
              that instead of sixteen identical invites to click. Only an
              actual edit lights it up. */}
          {unchanged ? (
            <Button
              size='sm'
              variant='outline'
              className='opacity-40'
              disabled
              title='修改设置后可保存'
            >
              保存
            </Button>
          ) : (
            <Button
              size='sm'
              disabled={pending || inert || !selected.length}
              onClick={() => onSave({ enabled, stream, fps, labels: selected, sensitivity })}
            >
              保存*
            </Button>
          )}
          <WorkbenchToggle
            open={workbenchOpen}
            disabled={inert}
            label={label}
            channelID={channel.id ?? ''}
            onToggle={() => setWorkbenchOpen(!workbenchOpen)}
          />
        </div>
      </div>
      {dirty && <StatusBadge tone='warning'>未保存</StatusBadge>}
      {workbenchOpen && (
        <ZoneWorkbench channelID={channel.id ?? ''} detectionEnabled={enabled} />
      )}
    </div>
  )

  function setStream(next: string | undefined) {
    mutate({ stream: (next ?? 'sub') as 'main' | 'sub' })
  }
  function setFps(next: string | undefined) {
    mutate({ fps: Number(next ?? 5) })
  }
}

/** The zones/rules count lives on the toggle so fences are visible unexpanded. */
function WorkbenchToggle({
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
    open || (!!channelID && !disabled)
  )
  const rules = useAPI<Schema<'DetectionRulePage'>>(
    `/api/v1/detection/rules?channel_id=${channelID}`,
    open || (!!channelID && !disabled)
  )
  const zoneCount = zones.data?.items?.length
  const ruleCount = rules.data?.items?.length
  // The counts ride on the accessible name too: an explicit aria-label would
  // otherwise override the visible text and hide the numbers from screen
  // readers and queries alike.
  const text = open
    ? '收起区域与规则'
    : `区域与规则${zoneCount != null && ruleCount != null ? ` (${zoneCount} 区域 · ${ruleCount} 规则)` : ''}`
  return (
    <Button
      type='button'
      size='sm'
      variant='ghost'
      aria-expanded={open}
      aria-label={`${label} ${text}`}
      disabled={disabled}
      onClick={onToggle}
    >
      {text}
    </Button>
  )
}
