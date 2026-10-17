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
import { useState } from 'react'
import { useAPI } from './hooks'
import { Panel, QueryState, PageTitle } from './ui'
import { formatClock } from '@/features/playback/zone'
import { AbnormalChannels } from './abnormal-channels'

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

const PROBE_LABELS: Record<string, string> = {
  probe_succeeded: '探测成功',
  probe_failed: '探测失败',
  sampler_running: '采样运行中',
  sampler_stopped: '采样已停止',
  feature_disabled: '未配置归档 / 按需启用',
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
  // The patrol's right column: the latest detection events, media states
  // shown honestly, straight into playback on click.
  const [eventWindow] = useState(() => {
    const end = Date.now()
    return { start: end - 7 * 86_400_000, end }
  })
  const recentEvents = useAPI<Schema<'EventPage'>>(
    `/api/v1/events?${new URLSearchParams({
      start: String(eventWindow.start),
      end: String(eventWindow.end),
      limit: '4',
    })}`
  )
  const zone = site.data?.timezone || ''
  const channels = summaries.data?.items || []
  // Event rows name their channel via this map.
  const channelOf = new Map(
    channels.map((item) => [item.channel_id, item])
  )
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
  // Capacity facts come from the pool's own probes (total/free per service).
  const poolCheck = defaultPool?.checks?.find(
    (check) => check.service === 'api' || check.service === 'worker'
  )
  const poolFree = poolCheck?.free_bytes ?? 0
  const poolTotal = poolCheck?.total_bytes ?? 0
  const gib = (value: number) => (value / 1024 / 1024 / 1024).toFixed(1) + ' GiB'

  return (
    <div className='space-y-8'>
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
            className='group rounded-xl border bg-card p-4 shadow-2xs transition-colors hover:border-zinc-300 hover:shadow-sm dark:hover:border-emerald-500/40'
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
            className='group rounded-xl border bg-card p-4 shadow-2xs transition-colors hover:border-zinc-300 hover:shadow-sm dark:hover:border-emerald-500/40'
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
            className='group rounded-xl border bg-card p-4 shadow-2xs transition-colors hover:border-zinc-300 hover:shadow-sm dark:hover:border-emerald-500/40'
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
                <div className='mt-2 flex items-start justify-between gap-2'>
                  <p className='text-3xl font-semibold tabular-nums'>
                    {poolTotal > 0 ? (
                      <>
                        {gib(poolFree)}
                        <span className='ms-1 text-sm font-normal text-muted-foreground'>
                          可用
                        </span>
                      </>
                    ) : (
                      <span
                        className={
                          defaultPool.state === 'ready'
                            ? 'text-emerald-600 dark:text-emerald-400'
                            : 'text-amber-600 dark:text-amber-400'
                        }
                      >
                        {
                          { ready: '就绪', pending: '待验证', unavailable: '不可用', low_space: '空间不足', disabled: '已停用' }[
                            defaultPool.state
                          ]
                        }
                      </span>
                    )}
                  </p>
                  <span
                    className={cn(
                      'inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs',
                      defaultPool.state === 'ready'
                        ? 'border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
                        : 'border-amber-500/20 bg-amber-500/10 text-amber-600 dark:text-amber-400'
                    )}
                  >
                    {
                      { ready: '就绪', pending: '待验证', unavailable: '不可用', low_space: '空间不足', disabled: '已停用' }[
                        defaultPool.state
                      ]
                    }
                  </span>
                </div>
                <p className='mt-2 truncate text-xs text-muted-foreground' title={defaultPool.name}>
                  {defaultPool.name}
                  {defaultPool.is_default ? ' · 默认池' : ''}
                  {poolTotal > 0 ? ` / 共 ${gib(poolTotal)}` : ''}
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
          <p className='mb-3 text-xs font-medium uppercase tracking-wide text-muted-foreground'>
            每 5 秒刷新 · 正常 / 异常 / 未启用
          </p>
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
                    'relative rounded-lg border p-3 bg-zinc-50/70 transition-colors hover:bg-zinc-100/80 dark:bg-zinc-900/50 dark:hover:bg-zinc-900/80',
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
      {/* The quick patrol: live channels on the left, the latest detection
          events on the right — the bottom band stays balanced at any count. */}
      {/* Abnormal channels sit above the patrol: the operator's eye should land
          on what needs attention before the list of healthy channels. */}
      {site.data && <AbnormalChannels />}
      {site.data && (
        <Panel title='活跃通道与快捷巡检'>
          <div className='grid gap-6 lg:grid-cols-2'>
            <section>
              <p className='mb-3 text-xs font-medium uppercase tracking-wide text-muted-foreground'>
                活跃通道巡检
              </p>
              {active.length === 0 ? (
                <p className='text-sm text-muted-foreground'>
                  暂无在线通道。接入摄像头后，这里会显示每一路的状态与快捷入口。
                </p>
              ) : (
                <div className='grid gap-3 sm:grid-cols-2'>
              {active.map((channel) => {
                const label = `CH${String(channel.channel_no).padStart(2, '0')}`
                return (
                  <div
                    key={channel.channel_id}
                    data-testid='active-channel-card'
                    className='rounded-lg border bg-zinc-50/70 p-3 transition-colors hover:bg-zinc-100/80 dark:bg-zinc-900/50 dark:hover:bg-zinc-900/80'
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
            </section>
            <section>
              <p className='mb-3 flex items-center justify-between text-xs font-medium uppercase tracking-wide text-muted-foreground'>
                最近 AI 事件
                <Link
                  to='/events'
                  className='inline-flex items-center gap-1 font-normal normal-case text-muted-foreground hover:text-foreground'
                >
                  查看全部事件 →
                </Link>
              </p>
              {(recentEvents.data?.items || []).length === 0 ? (
                <p className='text-sm text-muted-foreground'>
                  最近 7 天没有检测事件。
                </p>
              ) : (
                <ul className='space-y-2'>
                  {(recentEvents.data?.items || []).slice(0, 4).map((item) => {
                    const channel = channelOf.get(item.channel_id ?? '')
                    const label = channel
                      ? `CH${String(channel.channel_no ?? 0).padStart(2, '0')} ${channel.channel_name}`
                      : '未知通道'
                    const seconds = item.ended_at
                      ? Math.max(1, Math.round((Date.parse(item.ended_at) - Date.parse(item.started_at ?? '')) / 1000))
                      : null
                    return (
                      <li key={item.id}>
                        <Link
                          to='/events'
                          data-testid='overview-event-row'
                          className='flex items-center justify-between gap-2 rounded-lg border bg-zinc-50/70 p-2 text-xs transition-colors hover:bg-zinc-100/80 dark:bg-zinc-900/50 dark:hover:bg-zinc-900/80'
                        >
                          <span className='flex min-w-0 items-center gap-2'>
                            <span className='shrink-0 rounded bg-zinc-200/70 px-1.5 py-0.5 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300'>
                              {CATEGORY_LABELS[item.category ?? ''] || item.category}
                            </span>
                            <span className='truncate text-foreground'>
                              {label}
                              {item.zones?.length ? ` · ${item.zones.join('/')}` : ''}
                            </span>
                          </span>
                          <span className='shrink-0 tabular-nums text-muted-foreground'>
                            {formatClock(Date.parse(item.started_at ?? ''), zone).slice(5)}
                            {seconds != null ? ` · ${seconds}s` : ''}
                          </span>
                        </Link>
                      </li>
                    )
                  })}
                </ul>
              )}
            </section>
          </div>
        </Panel>
      )}
    </div>
  )
}
