import { Link } from '@tanstack/react-router'
import {
  Activity,
  ArrowRight,
  Cctv,
  Database,
  HardDrive,
  Play,
  ScanEye,
  Video,
} from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import type { Schema } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useAPI } from './hooks'
import { Panel, QueryState, PageTitle } from './ui'

type Summary = Schema<'ChannelSummary'>
type Component = Schema<'Component'>

// The component display names live here, not in the API: the wire contract
// stays machine-stable while the operator reads Chinese labels.
const COMPONENT_META: Record<string, { name: string; role: string }> = {
  gateway: { name: 'Gateway', role: '网关服务' },
  api: { name: 'API Server', role: '核心接口' },
  worker: { name: 'Worker', role: '后台任务与采样' },
  postgres: { name: 'PostgreSQL', role: '主数据库' },
  zlm: { name: 'ZLMediaKit', role: '流媒体服务' },
  frigate: { name: 'Frigate', role: 'AI 视觉检测' },
  mqtt: { name: 'MQTT Broker', role: '消息总线' },
  openlist: { name: 'OpenList', role: '外部归档挂载' },
}

// One badge vocabulary for the matrix: the breathing dot is the at-a-distance
// signal, the label names the state in plain words.
function componentBadge(component: Component) {
  if (!component.enabled)
    return {
      label: '未启用',
      badge: 'bg-zinc-500/10 text-zinc-400 border-zinc-500/20',
      dot: 'bg-zinc-500',
      pulse: false,
    }
  switch (component.state) {
    case 'healthy':
      return {
        label: '正常',
        badge: 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20',
        dot: 'bg-emerald-400',
        pulse: true,
      }
    case 'unavailable':
      return {
        label: '异常',
        badge: 'bg-red-500/10 text-red-400 border-red-500/20',
        dot: 'bg-red-400',
        pulse: true,
      }
    default:
      return {
        label: '未知',
        badge: 'bg-amber-500/10 text-amber-400 border-amber-500/20',
        dot: 'bg-amber-400',
        pulse: false,
      }
  }
}

const PROBE_LABELS: Record<string, string> = {
  probe_succeeded: '探测成功',
  probe_failed: '探测失败',
  sampler_running: '采样运行中',
  sampler_stopped: '采样已停止',
}

function probeLabel(reason: string) {
  if (!reason) return ''
  // Reason codes travel as space-separated probe facts; name each one.
  return reason
    .split(/\s+/)
    .map((part) => PROBE_LABELS[part] || part)
    .join(' · ')
}

export function Overview() {
  const user = useAuthStore((s) => s.user)
  const admin = user?.role === 'admin'
  const site = useAPI<Schema<'Site'>>('/api/v1/site')
  const summaries = useAPI<{ items: Summary[] }>('/api/v1/channels/summary')
  const runtime = useAPI<Schema<'DetectionRuntime'>>(
    '/api/v1/detection/runtime'
  )
  const pools = useAPI<{ items: Schema<'Pool'>[] }>(
    '/api/v1/storage-pools?limit=100'
  )
  const components = useAPI<Component[]>(
    '/api/v1/operations/components',
    admin,
    5000
  )
  const channels = summaries.data?.items || []
  // Same derivation as the channels page's status pills, so the overview and
  // the management table can never disagree about a row.
  const isUnconfigured = (channel: Summary) =>
    !channel.source_ip &&
    !channel.current_revision_id &&
    channel.main.state === 'not_configured'
  const online = channels.filter(
    (channel) => channel.enabled && channel.main.state === 'healthy'
  ).length
  const disabled = channels.filter((channel) => !channel.enabled).length
  const unconfigured = channels.filter(isUnconfigured).length
  const detectionEnabled = channels.filter(
    (channel) => channel.enabled && !isUnconfigured(channel) && channel.main.state === 'healthy'
  )
  const configured = channels.filter(
    (channel) => !isUnconfigured(channel)
  )
  const active = configured
    .filter((channel) => channel.main.state === 'healthy')
    .slice(0, 8)
  const healthyComponents =
    components.data?.filter(
      (component) => component.enabled && component.state === 'healthy'
    ).length ?? 0
  const enabledComponents =
    components.data?.filter((component) => component.enabled).length ?? 0
  const defaultPool = pools.data?.items?.find((pool) => pool.is_default) ||
    pools.data?.items?.[0]

  return (
    <>
      <PageTitle
        title='总览'
        description={
          site.data
            ? `${site.data.name} · ${site.data.timezone} · ${site.data.channel_count} 路固定通道`
            : '站点配置与服务状态'
        }
      />
      <QueryState
        pending={site.isPending || summaries.isPending}
        error={site.error || summaries.error}
        retry={() => {
          void site.refetch()
          void summaries.refetch()
        }}
      />
      {site.data && (
        <div className='grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4'>
          {/* Channels: the headline number is what the operator patrols with. */}
          <Link
            to='/channels'
            search={{ batch: '' }}
            className='group rounded-xl border bg-card p-4 transition-colors hover:border-emerald-500/40'
          >
            <div className='flex items-center justify-between'>
              <p className='flex items-center gap-1.5 text-xs text-muted-foreground'>
                <Video aria-hidden className='size-3.5' />
                通道概览
              </p>
              <ArrowRight
                aria-hidden
                className='size-4 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100'
              />
            </div>
            <p className='mt-2 text-3xl font-semibold tabular-nums'>
              {online}
              <span className='ms-1 text-sm font-normal text-muted-foreground'>
                / {channels.length} 路在线
              </span>
            </p>
            <p className='mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs'>
              <span className='inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400'>
                <span aria-hidden className='size-1.5 rounded-full bg-emerald-500' />
                在线 {online}
              </span>
              <span className='inline-flex items-center gap-1 text-muted-foreground'>
                <span aria-hidden className='size-1.5 rounded-full bg-zinc-400' />
                已停用 {disabled}
              </span>
              <span className='inline-flex items-center gap-1 text-zinc-500'>
                <span aria-hidden className='size-1.5 rounded-full bg-zinc-700' />
                未配置 {unconfigured}
              </span>
            </p>
          </Link>
          {/* Detection engine: live facts from the detector, never invented. */}
          <Link
            to='/detection'
            className='group rounded-xl border bg-card p-4 transition-colors hover:border-emerald-500/40'
          >
            <div className='flex items-center justify-between'>
              <p className='flex items-center gap-1.5 text-xs text-muted-foreground'>
                <ScanEye aria-hidden className='size-3.5' />
                AI 检测引擎
              </p>
              <ArrowRight
                aria-hidden
                className='size-4 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100'
              />
            </div>
            {runtime.data?.runtime?.reachable ? (
              <>
                <p className='mt-2 text-3xl font-semibold tabular-nums'>
                  {runtime.data.runtime.inference_ms
                    ? `${runtime.data.runtime.inference_ms.toFixed(1)}`
                    : '—'}
                  <span className='ms-1 text-sm font-normal text-muted-foreground'>
                    ms 推理
                  </span>
                </p>
                <p className='mt-2 text-xs text-muted-foreground'>
                  {runtime.data.runtime.detector || '—'}
                  {runtime.data.runtime.overloaded ? ' · 过载' : ''} ·{' '}
                  {detectionEnabled.length} 路检测中
                </p>
              </>
            ) : (
              <>
                <p className='mt-2 text-3xl font-semibold text-muted-foreground'>
                  未运行
                </p>
                <p className='mt-2 text-xs text-muted-foreground'>
                  检测服务不可达或未启用 · {detectionEnabled.length} 路待接入
                </p>
              </>
            )}
          </Link>
          {/* Recording & storage: pool health plus the shortcut that matters. */}
          <Link
            to='/recordings'
            search={{ channel: '', at: '', tab: '' }}
            className='group rounded-xl border bg-card p-4 transition-colors hover:border-emerald-500/40'
          >
            <div className='flex items-center justify-between'>
              <p className='flex items-center gap-1.5 text-xs text-muted-foreground'>
                <HardDrive aria-hidden className='size-3.5' />
                录像与存储
              </p>
              <ArrowRight
                aria-hidden
                className='size-4 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100'
              />
            </div>
            {defaultPool ? (
              <>
                <p
                  className={cn(
                    'mt-2 text-3xl font-semibold',
                    defaultPool.state === 'ready'
                      ? 'text-emerald-600 dark:text-emerald-400'
                      : 'text-amber-600 dark:text-amber-400'
                  )}
                >
                  {
                    { ready: '就绪', pending: '待验证', unavailable: '不可用', low_space: '空间不足', disabled: '已停用' }[
                      defaultPool.state
                    ]
                  }
                </p>
                <p className='mt-2 truncate text-xs text-muted-foreground' title={defaultPool.name}>
                  {defaultPool.name}
                  {defaultPool.is_default ? ' · 默认池' : ''}
                </p>
              </>
            ) : (
              <>
                <p className='mt-2 text-3xl font-semibold text-muted-foreground'>—</p>
                <p className='mt-2 text-xs text-muted-foreground'>尚无存储池</p>
              </>
            )}
          </Link>
          {/* Site identity and the one-number component verdict. */}
          <div className='rounded-xl border bg-card p-4'>
            <p className='flex items-center gap-1.5 text-xs text-muted-foreground'>
              <Activity aria-hidden className='size-3.5' />
              站点与系统
            </p>
            <p className='mt-2 text-3xl font-semibold tabular-nums'>
              {enabledComponents > 0 ? `${healthyComponents}` : '—'}
              <span className='ms-1 text-sm font-normal text-muted-foreground'>
                / {enabledComponents} 核心服务正常
              </span>
            </p>
            <p className='mt-2 flex items-center gap-1 text-xs text-muted-foreground'>
              <Database aria-hidden className='size-3' />
              {site.data.timezone}
            </p>
          </div>
        </div>
      )}
      {admin && (
        <Panel title='组件状态'>
          <QueryState
            pending={components.isPending}
            error={components.error}
            retry={() => components.refetch()}
          />
          <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-4'>
            {components.data?.map((component) => {
              const badge = componentBadge(component)
              const meta = COMPONENT_META[component.name] || {
                name: component.name,
                role: '',
              }
              return (
                <div
                  key={component.name}
                  data-testid='component-card'
                  data-state={component.enabled ? component.state : 'disabled'}
                  className={cn(
                    'relative rounded-lg border p-3',
                    component.enabled
                      ? component.state === 'healthy'
                        ? 'border-emerald-500/20'
                        : component.state === 'unavailable'
                          ? 'border-red-500/40'
                          : 'border-amber-500/40'
                      : 'border-dashed opacity-60'
                  )}
                >
                  <div className='flex items-start justify-between gap-2'>
                    <div className='min-w-0'>
                      <p className='truncate font-medium'>{meta.name}</p>
                      <p className='truncate text-xs text-muted-foreground'>
                        {meta.role}
                      </p>
                    </div>
                    <span
                      className={cn(
                        'inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs',
                        badge.badge
                      )}
                    >
                      <span
                        aria-hidden
                        className={cn(
                          'size-1.5 rounded-full',
                          badge.dot,
                          badge.pulse && 'animate-pulse'
                        )}
                      />
                      {badge.label}
                    </span>
                  </div>
                  {component.reason && (
                    <p
                      className='mt-2 truncate font-mono text-[11px] text-zinc-500'
                      title={probeLabel(component.reason)}
                    >
                      {probeLabel(component.reason) || component.reason}
                    </p>
                  )}
                </div>
              )
            })}
          </div>
        </Panel>
      )}
      {/* The quick patrol: configured channels with their live facts and the
          two shortcuts the operator actually uses. */}
      {site.data && (
        <Panel title='活跃通道与快捷巡检'>
          {active.length === 0 ? (
            <p className='text-sm text-muted-foreground'>
              暂无在线通道。接入摄像头后，这里会显示每一路的状态与快捷入口。
            </p>
          ) : (
            <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-4'>
              {active.map((channel) => {
                const label = `CH${String(channel.channel_no).padStart(2, '0')}`
                return (
                  <div
                    key={channel.channel_id}
                    data-testid='active-channel-card'
                    className='rounded-lg border p-3'
                  >
                    <div className='flex items-center justify-between gap-2'>
                      <p className='flex min-w-0 items-center gap-1.5'>
                        <Cctv aria-hidden className='size-4 shrink-0 text-emerald-500' />
                        <span className='truncate font-medium'>{channel.channel_name}</span>
                      </p>
                      <span className='shrink-0 font-mono text-xs text-muted-foreground'>
                        {label}
                      </span>
                    </div>
                    <p className='mt-1.5 text-xs text-muted-foreground'>
                      主流正常
                      {channel.bitrate_kbps
                        ? ` · ${channel.bitrate_kbps >= 1000 ? `${(channel.bitrate_kbps / 1000).toFixed(1)} Mbps` : `${Math.round(channel.bitrate_kbps)} kbps`}`
                        : ''}
                    </p>
                    <div className='mt-2 flex gap-1.5'>
                      {channel.permissions.includes('live') && (
                        <Link
                          to='/live'
                          search={{ channel: channel.channel_id }}
                          className='inline-flex h-6 items-center gap-1 rounded-md border px-2 text-xs hover:bg-accent'
                        >
                          <Play aria-hidden className='size-3' />
                          预览
                        </Link>
                      )}
                      {channel.permissions.includes('playback') && (
                        <Link
                          to='/recordings'
                          search={{ channel: channel.channel_id, at: '', tab: 'playback' }}
                          className='inline-flex h-6 items-center gap-1 rounded-md border px-2 text-xs hover:bg-accent'
                        >
                          <Video aria-hidden className='size-3' />
                          回放
                        </Link>
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </Panel>
      )}
    </>
  )
}
