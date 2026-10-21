import { useMemo, useState } from 'react'
import { configuredChannels } from '@/features/foundation/channels'
import { useQueryClient } from '@tanstack/react-query'
import { apiRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { LoadingButton } from '@/features/foundation/loading-button'
import { StatusBadge } from '@/features/foundation/status-badge'
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

const HEALTH_BADGES: Record<
  RowHealth,
  { tone: 'success' | 'warning' | 'neutral' | 'destructive'; text: string }
> = {
  online: { tone: 'success', text: '在线' },
  offline: { tone: 'warning', text: '离线' },
  disabled: { tone: 'neutral', text: '通道已停用' },
  unconfigured: { tone: 'neutral', text: '未配置源' },
  unknown: { tone: 'neutral', text: '未知' },
}

type RowFilter = 'all' | 'enabled' | 'unconfigured'
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
        <div className='grid gap-2 rounded-xl border bg-card p-4 text-sm sm:grid-cols-2 lg:grid-cols-4'>
          <div>
            <p className='text-xs text-muted-foreground'>检测服务</p>
            <p>
              <StatusBadge tone={runtime.data.runtime?.reachable ? 'success' : 'destructive'}>
                {runtime.data.runtime?.reachable ? '运行中' : '不可达'}
              </StatusBadge>
            </p>
          </div>
          <div>
            <p className='text-xs text-muted-foreground'>版本 / 检测器</p>
            <p>
              {runtime.data.runtime?.version || '—'} · {runtime.data.runtime?.detector || '—'}
            </p>
          </div>
          <div>
            <p className='text-xs text-muted-foreground'>推理耗时</p>
            <p>
              {runtime.data.runtime?.inference_ms
                ? `${runtime.data.runtime.inference_ms.toFixed(1)} ms`
                : '—'}
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
                {runtime.data.revision.pending ? '有设置尚未发布' : '与检测服务一致'}
              </p>
            )}
          </div>
        </div>
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
          <div
            role='group'
            aria-label='通道筛选'
            className='flex flex-wrap items-center gap-1'
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
          <div className='overflow-hidden rounded-xl border bg-card'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className='w-20'>通道</TableHead>
                  <TableHead className='w-24'>状态</TableHead>
                  <TableHead className='w-24'>检测</TableHead>
                  <TableHead className='w-24'>码流</TableHead>
                  <TableHead className='w-20'>帧率</TableHead>
                  <TableHead className='w-24'>灵敏度</TableHead>
                  <TableHead>目标</TableHead>
                  <TableHead className='w-40'>区域与规则</TableHead>
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
          className='sticky bottom-0 z-20 -mx-4 flex flex-wrap items-center justify-between gap-3 border-t border-amber-600/40 bg-card/95 px-4 py-2.5 backdrop-blur'
        >
          <span className='text-sm text-amber-600'>
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
  badge: { tone: 'success' | 'warning' | 'neutral' | 'destructive'; text: string }
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
        className={cn(
          interactionClass(interactive, selected),
          inert && 'opacity-60'
        )}
        onClick={() => interactive && onSelect(!selected)}
      >
        <TableCell className='font-medium'>{label}</TableCell>
        <TableCell>
          <StatusBadge tone={badge.tone}>{badge.text}</StatusBadge>
        </TableCell>
        <TableCell>
          <StatusBadge tone={enabled ? 'success' : 'neutral'}>
            {enabled ? '已启用' : '未启用'}
          </StatusBadge>
        </TableCell>
        <TableCell className='text-sm'>{STREAM_TEXT[stream] ?? stream}</TableCell>
        <TableCell className='tabular-nums text-sm'>{fps} fps</TableCell>
        <TableCell className='text-sm'>
          {SENSITIVITY_TEXT[sensitivity as keyof typeof SENSITIVITY_TEXT] ?? sensitivity}
        </TableCell>
        <TableCell className='text-sm'>
          {configSummary(labels)}
          {inert && (
            <span className='ml-2 text-xs text-muted-foreground'>请先在通道管理中接入摄像头</span>
          )}
        </TableCell>
        <TableCell onClick={(event) => event.stopPropagation()}>
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
          <TableCell colSpan={8} className='bg-card/40 p-0'>
            <ZoneWorkbench channelID={channel.id ?? ''} detectionEnabled={enabled} />
          </TableCell>
        </TableRow>
      )}
    </>
  )
}

function interactionClass(interactive: boolean, selected: boolean) {
  if (!interactive) return 'cursor-not-allowed'
  return cn('cursor-pointer transition-colors', selected ? 'bg-primary/10 hover:bg-primary/15' : 'hover:bg-accent/50')
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
