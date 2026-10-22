import { Link } from '@tanstack/react-router'
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  HardDrive,
  ImageOff,
  Play,
  ScanEye,
  Video,
} from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import type { Schema } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useState } from 'react'
import { useAPI } from './hooks'
import { QueryState, PageTitle } from './ui'
import { formatClock } from '@/features/playback/zone'
import { durationLabel } from '@/features/channels/reliability-format'

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
  // DASH-02 host figures. Polled on the same cadence as the component matrix:
  // they describe the same machine, and two different refresh rates would let
  // the card disagree with itself.
  const resources = useAPI<Schema<'HostResources'>>(
    '/api/v1/operations/resources',
    admin,
    5000
  )
  // The patrol's right column: the latest detection events, media states
  // shown honestly, straight into playback on click.
  // Pictures that failed to load, so the list falls back to the icon instead of
  // the browser's broken-image glyph.
  const [failedThumbs, setFailedThumbs] = useState<Set<string>>(new Set())
  const [eventWindow] = useState(() => {
    const end = Date.now()
    return { start: end - 7 * 86_400_000, end }
  })
  const recentEvents = useAPI<Schema<'EventPage'>>(
    `/api/v1/events?${new URLSearchParams({
      start: String(eventWindow.start),
      end: String(eventWindow.end),
      limit: '8',
    })}`
  )
  // DASH-04: availability, recording coverage and the channels that were not
  // available, over the last day. Administrator-only because comparing channels
  // means reading media evidence about all of them.
  const [reliabilityWindow] = useState(() => {
    const to = new Date()
    const from = new Date(to.getTime() - 24 * 3600_000)
    return { from: from.toISOString(), to: to.toISOString() }
  })
  const reliability = useAPI<{ items: Schema<'ReliabilityChannelSummary'>[] }>(
    `/api/v1/reliability/summary?from=${reliabilityWindow.from}&to=${reliabilityWindow.to}`,
    admin
  )
  // The faults themselves, not just a count: "1 路有中断" tells an operator
  // something is wrong and nothing about which channel, which is the one thing
  // they need next. These rows carry the channel and the exact span.
  const faults = useAPI<{ items: Schema<'ReliabilityFault'>[] }>(
    `/api/v1/faults?from=${reliabilityWindow.from}&to=${reliabilityWindow.to}`,
    admin
  )
  // One entry per channel that had an interruption, newest fault first: the
  // strip names the worst channel and the cards each carry their own history.
  const faultsByChannel = new Map<string, Schema<'ReliabilityFault'>>()
  for (const fault of faults.data?.items || []) {
    if (fault.maintenance) continue
    const id = fault.channel_id ?? ''
    if (!id) continue
    const seen = faultsByChannel.get(id)
    const started = Date.parse(fault.started_at ?? '')
    if (!seen || started > Date.parse(seen.started_at ?? '')) {
      faultsByChannel.set(id, fault)
    }
  }
  const zone = site.data?.timezone || ''
  const channels = summaries.data?.items || []
  // Event rows name their channel via this map.
  const channelOf = new Map(
    channels.map((item) => [item.channel_id, item])
  )
  // The fault rows carry a channel number but not its name (the contract omits
  // it), so the alarm strip resolves the name from the summary it already has.
  const channelNameOf = new Map(
    channels.map((item) => [item.channel_id, item.channel_name])
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
  const faultOf = (id: string | undefined) =>
    id ? faultsByChannel.get(id) ?? null : null
  const active = configured
    .filter((channel) => channel.main.state === 'healthy')
    .slice(0, 8)
  // DASH-04 figures, derived from the same cross-channel summary the operations
  // page reads, so the two views cannot disagree about a channel.
  //
  // Availability is reported as a duration-derived percentage with its own
  // denominator, and only for channels whose sample is sufficient: quoting a
  // figure from ten minutes of history as "the site's availability" is the
  // unfounded number PRD REL-05 forbids.
  const reliable = (reliability.data?.items || []).filter(
    (row) => row.sample_sufficient && row.observed_ms > 0
  )
  const sumOf = (kind: string, field: 'healthy_ms' | 'observed_ms') =>
    reliable
      .filter((row) => row.kind === kind)
      .reduce((total, row) => total + row[field], 0)
  const mainObserved = sumOf('main', 'observed_ms')
  const mainAvailability =
    mainObserved > 0
      ? {
          percent: `${((sumOf('main', 'healthy_ms') / mainObserved) * 100).toFixed(1)}%`,
          observed: mainObserved,
        }
      : null
  const recordingOwed = reliable
    .filter((row) => row.kind === 'recording')
    .reduce((total, row) => total + row.owed_ms, 0)
  const recordingCovered = reliable
    .filter((row) => row.kind === 'recording')
    .reduce((total, row) => total + row.covered_ms, 0)
  const recordingCoverage =
    recordingOwed > 0
      ? `${((recordingCovered / recordingOwed) * 100).toFixed(1)}%`
      : null
  // A channel counts as abnormal when it had a real interruption in the window;
  // unavailable time is the fact, not a threshold crossing.
  const abnormalChannels = new Set(
    reliable
      .filter((row) => row.kind === 'main' && row.unavailable_ms > 0)
      .map((row) => row.channel_id)
  ).size
  // Channels with too little observation to judge are counted separately: they
  // are not evidence of a fault, and folding them into "abnormal" would name
  // healthy cameras as failing ones.
  const unjudgedChannels = (reliability.data?.items || []).filter(
    (row) => row.kind === 'main' && !row.sample_sufficient
  ).length
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
        // Four equal KPI columns. The host-resource card moved out of this row:
        // squeezed into a fifth column it held four meters and two lines of
        // counters, which is a panel's worth of detail in a KPI slot. It now
        // sits beside the component matrix, where "what is the machine doing"
        // belongs next to "are the services up".
        <div className='grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4'>
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
          {/* DASH-04: what the last 24 hours actually looked like, per stream
              kind. Administrator-only because comparing channels reads media
              evidence about all of them; a viewer sees no card rather than a
              card full of zeros. */}
          {admin && (
            <Link
              to='/operations'
              className='group rounded-xl border bg-card p-4 shadow-2xs transition-colors hover:border-zinc-300 hover:shadow-sm dark:hover:border-emerald-500/40'
            >
              <div className='flex items-center justify-between'>
                <p className='flex items-center gap-1.5 text-xs text-muted-foreground'>
                  <Activity aria-hidden className='size-3.5' />
                  24 小时可用性
                </p>
                <ArrowRight
                  aria-hidden
                  className='size-4 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100'
                />
              </div>
              {mainAvailability ? (
                <>
                  <p className='mt-2 text-3xl font-semibold tabular-nums'>
                    {mainAvailability.percent}
                    <span className='ms-1 text-sm font-normal text-muted-foreground'>
                      主流
                    </span>
                  </p>
                  <p className='mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs'>
                    {/* Coverage is a ratio against OWED time, not against the
                        clock. A site where most channels record only on events
                        owes little, so a low percentage is not a fault — but
                        painted green beside "无中断" it read as a contradiction.
                        Neutral colour plus the denominator says what it means. */}
                    <span
                      className='text-muted-foreground'
                      title='已覆盖时长 ÷ 应录时长。只在计划或连续录像的通道计入分母，因此事件录像为主的站点数值天然偏低。'
                    >
                      录像覆盖 {recordingCoverage ?? '—'}
                      <span className='text-muted-foreground/60'>（按应录时长）</span>
                    </span>
                    {/* The interruption count lives in the alarm pill on the
                        status strip. Printing it here as well made the same
                        fact appear twice on one page, and the card's job is the
                        availability rate. */}
                  </p>
                </>
              ) : (
                <>
                  <p className='mt-2 text-3xl font-semibold text-muted-foreground'>
                    —
                  </p>
                  <p className='mt-2 text-xs text-muted-foreground'>
                    {reliability.isPending ? '统计中' : '窗口内观测不足'}
                  </p>
                </>
              )}
            </Link>
          )}
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
                {poolTotal > 0 && (
                  /* A 4px bar under the figure: "346 GiB free" answers "is there
                     room", the bar answers "how full is it" without arithmetic. */
                  <div
                    className='mt-2 h-1 w-full overflow-hidden rounded-full bg-muted'
                    role='img'
                    aria-label={`已用 ${((1 - poolFree / poolTotal) * 100).toFixed(0)}%`}
                  >
                    <div
                      className={cn(
                        'h-full rounded-full',
                        (1 - poolFree / poolTotal) * 100 >= 85
                          ? 'bg-amber-500'
                          : 'bg-emerald-500'
                      )}
                      style={{
                        width: `${Math.max(1, (1 - poolFree / poolTotal) * 100)}%`,
                      }}
                    />
                  </div>
                )}
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
        </div>
      )}
      {/* One strip, two poles: components on the left, the machine's summary
          figures in the middle, channel alarms hard right.
          The middle figures used to be a nested two-column block with its own
          heading, a second line of indicators and a third line for the cache
          detail — three rows of small text stacked inside half a strip, which is
          what made this band read as crowded and kept pushing the component
          pills onto a line of their own. */}
      {admin && (
        <div className='flex flex-wrap items-center justify-between gap-x-6 gap-y-3 rounded-xl border bg-card p-3'>
          <ComponentHealthBar components={components} />
          <div className='flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs tabular-nums text-muted-foreground'>
            <ResourceStat
              label='CPU'
              testId='host-cpu'
              value={
                resources.data?.cpu_known
                  ? `${Math.round(resources.data.cpu_percent)}%`
                  : resources.isPending
                    ? '…'
                    : '未知'
              }
            />
            <ResourceStat
              label='内存'
              testId='host-memory'
              value={
                resources.data?.memory_known
                  ? `${Math.round(resources.data.memory_percent)}% (${gibShort(resources.data.memory_used_bytes)}/${gibShort(resources.data.memory_total_bytes)})`
                  : resources.isPending
                    ? '…'
                    : '未知'
              }
            />
            <ResourceStat
              label='负载'
              value={
                resources.data?.load_known
                  ? String(resources.data.load_average)
                  : resources.isPending
                    ? '…'
                    : '未知'
              }
              testId='host-load'
            />
            {/* The cache only exists on deployments that reserved memory for it;
              elsewhere it is absent rather than zero. */}
            {resources.data && !resources.data.event_cache.configured && (
              <span
                data-testid='host-cache'
                className='inline-flex items-baseline gap-1 whitespace-nowrap'
              >
                <span>缓存盘</span>
                <span className='text-muted-foreground/70'>未启用</span>
              </span>
            )}
            {resources.data?.event_cache.configured && (
              <span
                data-testid='host-cache'
                className='inline-flex items-baseline gap-1 whitespace-nowrap'
              >
                <span>缓存盘</span>
                <span className='font-mono text-foreground'>
                  {resources.data.event_cache.known
                    ? `${Math.round(resources.data.event_cache.percent)}%`
                    : '未知'}
                </span>
                {resources.data.event_cache.known &&
                  resources.data.event_cache.percent >= 90 && (
                    <span className='text-amber-600 dark:text-amber-400'>接近上限</span>
                  )}
              </span>
            )}
          </div>
          <ChannelAlarms
            faults={faultsByChannel}
            abnormal={abnormalChannels}
            unjudged={unjudgedChannels}
            pending={reliability.isPending || faults.isPending}
            error={!!reliability.error || !!faults.error}
            zone={zone}
            channelNameOf={channelNameOf}
          />
        </div>
      )}
      {/* The quick patrol: live channels on the left, the latest detection
          events on the right — the bottom band stays balanced at any count. */}
      {/* Abnormal channels sit above the patrol: the operator's eye should land
          on what needs attention before the list of healthy channels. */}
      {site.data && (
        // No panel title: the two column headings are the titles. An outer
        // "活跃通道与快捷巡检" over a left heading "活跃通道巡检" said the same
        // thing twice and pushed both columns down.
        <div className='rounded-xl border bg-card p-4'>
          <div className='grid gap-6 lg:grid-cols-2'>
            <section>
              <p className='mb-3 text-sm font-medium'>活跃通道巡检</p>
              {active.length === 0 ? (
                <p className='text-sm text-muted-foreground'>
                  暂无在线通道。接入摄像头后，这里会显示每一路的状态与快捷入口。
                </p>
              ) : (
                /* Two columns of compact cards. A single column of 22px rows
                   left the left half of this band as bare background while the
                   event list on the right ran to eight rows — the two columns
                   were never the same shape. Two columns of cards fill the same
                   width with the same number of channels, and each card can hold
                   the channel's own detail without becoming a tile again. */
                <ul className='grid gap-2 sm:grid-cols-2'>
                  {active.map((channel) => {
                    const label = `CH${String(channel.channel_no).padStart(2, '0')}`
                    const streaming =
                      channel.main_media?.width && channel.main_media?.height
                        ? `${channel.main_media.width}×${channel.main_media.height}`
                        : ''
                    return (
                      <li
                        key={channel.channel_id}
                        data-testid='active-channel-card'
                        data-alert={faultOf(channel.channel_id) ? 'yes' : undefined}
                        className={cn(
                          'group flex items-center justify-between gap-3 rounded-lg border px-4 py-3.5 transition-colors',
                          faultOf(channel.channel_id)
                            ? // A channel that dropped out in the last day must
                              // not look identical to eight healthy ones: the
                              // whole point of the patrol is finding it by eye.
                              'border-amber-500/40 bg-amber-950/10 hover:border-amber-500/60'
                            : 'border-border bg-muted/30 hover:border-muted-foreground/40 hover:bg-muted/50'
                        )}
                      >
                        <div className='min-w-0'>
                          <p className='flex min-w-0 items-center gap-1.5'>
                            {/* Online now, but with a warning mark: the dot
                                answers "is it up this second", the mark answers
                                "has it been reliable today". Both are true and
                                neither replaces the other. */}
                            <span
                              aria-hidden
                              className={cn(
                                'size-1.5 shrink-0 rounded-full',
                                faultOf(channel.channel_id)
                                  ? 'bg-amber-500'
                                  : 'bg-emerald-500'
                              )}
                            />
                            <span className='shrink-0 font-mono text-[11px] text-muted-foreground'>
                              {label}
                            </span>
                            <span className='truncate text-sm font-medium'>
                              {channel.channel_name}
                            </span>
                            {faultOf(channel.channel_id) && (
                              <span
                                data-testid='channel-alert-badge'
                                title={faultTimeSpan(faultOf(channel.channel_id)!, zone)}
                                className='shrink-0 rounded border border-amber-500/40 px-1 text-[10px] text-amber-600 dark:text-amber-400'
                              >
                                {faultOf(channel.channel_id)!.state === 'open'
                                  ? '中断中'
                                  : `曾中断 ${durationLabel(faultOf(channel.channel_id)!.duration_ms ?? 0, 1)}`}
                              </span>
                            )}
                          </p>
                          {/* The metadata line carries all three facts an
                              operator scans for, in the order they read them:
                              what the main stream is, whether the sub stream is
                              healthy, and the bit rate. Two of them used to live
                              in different corners of the card. */}
                          <p className='mt-1 truncate pl-3 text-[11px] text-muted-foreground/70'>
                            主流 {streaming || '正常'}
                            {/* "子流正常" is a claim about right now, and the badge
                                beside the name already carries the history. When
                                the sub stream is not healthy the reason belongs
                                here; when it is, saying so adds nothing the dot
                                has not already said. */}
                            {channel.sub.state !== 'healthy'
                              ? ` · 子流${channel.sub.state === 'unavailable' ? '不可用' : '未观测'}`
                              : ''}
                            {channel.bitrate_kbps
                              ? ` · ${channel.bitrate_kbps >= 1000 ? `${(channel.bitrate_kbps / 1000).toFixed(1)} Mbps` : `${Math.round(channel.bitrate_kbps)} kbps`}`
                              : ''}
                          </p>
                        </div>
                        <div className='flex shrink-0 items-center gap-0.5'>
                          <span className='flex items-center gap-0.5 opacity-60 transition-opacity group-hover:opacity-100 focus-within:opacity-100'>
                            {channel.permissions.includes('live') && (
                              <Link
                                to='/live'
                                search={{ channel: channel.channel_id }}
                                aria-label={`预览 ${label}`}
                                title='预览'
                                className='grid size-6 place-items-center rounded text-muted-foreground hover:bg-accent hover:text-foreground'
                              >
                                <Play aria-hidden className='size-3' />
                              </Link>
                            )}
                            {channel.permissions.includes('playback') && (
                              <Link
                                to='/recordings'
                                search={{
                                  channel: channel.channel_id,
                                  at: '',
                                  tab: 'playback',
                                }}
                                aria-label={`回放 ${label}`}
                                title='回放'
                                className='grid size-6 place-items-center rounded text-muted-foreground hover:bg-accent hover:text-foreground'
                              >
                                <Video aria-hidden className='size-3' />
                              </Link>
                            )}
                          </span>
                        </div>
                      </li>
                    )
                  })}
                </ul>
              )}
            </section>
            <section>
              <p className='mb-3 flex items-center justify-between text-sm font-medium'>
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
                  {(recentEvents.data?.items || []).slice(0, 8).map((item) => {
                    const channel = channelOf.get(item.channel_id ?? '')
                    const label = channel
                      ? `CH${String(channel.channel_no ?? 0).padStart(2, '0')} ${channel.channel_name}`
                      : '未知通道'
                    // Three states, not two. "No end time" means the event is
                    // still running; an end time a fraction of a second after
                    // the start is a finished event shorter than the display
                    // resolution. The old Math.max(1, ...) reported both as
                    // "1s", which claimed a duration for an ongoing event.
                    const elapsedMs = item.ended_at
                      ? Date.parse(item.ended_at) - Date.parse(item.started_at ?? '')
                      : null
                    const durationLabel =
                      elapsedMs == null
                        ? '进行中'
                        : elapsedMs < 1000
                          ? '<1s'
                          : `${Math.round(elapsedMs / 1000)}s`
                    return (
                      <li key={item.id}>
                        <Link
                          to='/events'
                          data-testid='overview-event-row'
                          className='group flex items-center justify-between gap-2 rounded-lg border bg-zinc-50/70 p-2 text-xs transition-colors hover:border-zinc-300 hover:bg-zinc-100/80 dark:bg-zinc-900/50 dark:hover:border-emerald-500/30 dark:hover:bg-zinc-900/80'
                        >
                          <span className='flex min-w-0 items-center gap-2.5'>
                            {/* The crop is the fastest way to recognise an event:
                                a 40px thumbnail answers "what was it" before the
                                label is read. Only rendered when the picture is
                                actually there — a broken image on a healthy row
                                reads as a fault. */}
                            {item.snapshot_state === 'ready' &&
                            !failedThumbs.has(item.id ?? '') ? (
                              <img
                                src={`/api/v1/events/${item.id}/snapshot`}
                                alt=''
                                loading='lazy'
                                className='size-10 shrink-0 rounded-md border border-border object-cover'
                                // A stored picture can still fail to load (the
                                // detector was pruned). Falling back to the icon
                                // keeps a broken-image glyph out of the list.
                                onError={() =>
                                  setFailedThumbs((prev) =>
                                    new Set(prev).add(item.id ?? '')
                                  )
                                }
                              />
                            ) : (
                              <span className='grid size-10 shrink-0 place-items-center rounded-md border border-border bg-muted/40 text-muted-foreground/40'>
                                <ImageOff aria-hidden className='size-3.5' />
                              </span>
                            )}
                            {/* A tinted chip rather than a word: the category is
                                the fastest thing to scan in a list of events. */}
                            <span
                              className={cn(
                                'shrink-0 rounded px-1.5 py-0.5 text-[11px] font-medium',
                                item.category === 'person'
                                  ? 'bg-sky-500/15 text-sky-600 dark:text-sky-400'
                                  : item.category === 'car'
                                    ? 'bg-amber-500/15 text-amber-600 dark:text-amber-400'
                                    : 'bg-zinc-200/70 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300'
                              )}
                            >
                              {CATEGORY_LABELS[item.category ?? ''] || item.category}
                            </span>
                            <span className='min-w-0 flex-1 truncate text-foreground'>
                              {label}
                              {item.zones?.length ? ` · ${item.zones.join('/')}` : ''}
                            </span>
                          </span>
                          <span className='flex shrink-0 items-center gap-2 tabular-nums text-muted-foreground'>
                            <span className='whitespace-nowrap text-right font-mono text-xs'>
                              {formatClock(Date.parse(item.started_at ?? ''), zone).slice(5)}
                              {` · ${durationLabel}`}
                            </span>
                            {/* The row is a link; this is the affordance saying so.
                                It appears on hover to keep the row quiet at rest. */}
                            <span className='inline-flex items-center gap-0.5 text-[11px] opacity-0 transition-opacity group-hover:opacity-100'>
                              查看
                              <ArrowRight aria-hidden className='size-3' />
                            </span>
                          </span>
                        </Link>
                      </li>
                    )
                  })}
                </ul>
              )}
            </section>
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * Channel alarms for the status strip.
 *
 * Nothing alarming is stated plainly — "最近 24 小时无通道告警" — because a strip
 * that simply disappears when all is well leaves the operator unable to tell
 * "no alarms" from "the alarm query failed". A failed read says so instead of
 * claiming calm.
 */
function ChannelAlarms({
  faults,
  abnormal,
  unjudged,
  pending,
  error,
  zone,
  channelNameOf,
}: {
  /** The newest unplanned fault per channel, keyed by channel id. */
  faults: Map<string, Schema<'ReliabilityFault'>>
  abnormal: number
  unjudged: number
  pending: boolean
  error: boolean
  zone: string
  channelNameOf: Map<string, string>
}) {
  const entries = [...faults.values()]
  const nameOf = (id: string) => channelNameOf.get(id) ?? ''
  // Worst first: a channel still down outranks one that recovered, then by
  // length. The strip names one channel because naming five in a strip is a
  // table, and the cards below carry each channel's own history anyway.
  entries.sort((a, b) => {
    const open = Number(b.state === 'open') - Number(a.state === 'open')
    if (open !== 0) return open
    return (b.duration_ms ?? 0) - (a.duration_ms ?? 0)
  })
  const primary = entries[0]
  return (
    <section
      data-testid='channel-alarms'
      aria-label='通道告警'
      className='flex min-w-0 flex-col items-start gap-1.5 lg:items-end'
    >
      <p className='flex items-center gap-1.5 text-xs text-muted-foreground'>
        <AlertTriangle aria-hidden className='size-3.5' />
        通道告警 · 最近 24 小时
      </p>
      {error ? (
        <p className='text-xs text-muted-foreground'>告警数据读取失败</p>
      ) : pending ? (
        <p className='text-xs text-muted-foreground'>统计中…</p>
      ) : primary ? (
        <>
          {/* The channel's name, not a count. An operator who reads "1 路有中断"
              has to go hunting for which one; naming it makes the strip
              actionable, and the link lands on that channel's reliability tab. */}
          <Link
            to='/channels/configure'
            search={{ channel: primary.channel_id ?? '', tab: 'reliability' }}
            data-testid='alarm-channel'
            title={faultTimeSpan(primary, zone)}
            className='inline-flex max-w-full items-center gap-1.5 rounded-md border border-amber-500/40 bg-amber-500/10 px-2 py-1 text-xs text-amber-700 transition-colors hover:bg-amber-500/20 dark:text-amber-400'
          >
            <span className='relative flex size-1.5 shrink-0'>
              <span className='absolute inline-flex h-full w-full animate-ping rounded-full bg-amber-500 opacity-60 motion-reduce:hidden' />
              <span className='relative inline-flex size-1.5 rounded-full bg-amber-500' />
            </span>
            <span className='truncate font-medium'>
              CH{String(primary.channel_no ?? 0).padStart(2, '0')}
              {nameOf(primary.channel_id ?? '') ? ` ${nameOf(primary.channel_id ?? '')}` : ''}
            </span>
            <span className='shrink-0 tabular-nums'>
              {primary.state === 'open' ? '中断中' : '曾有中断'}{' '}
              {durationLabel(primary.duration_ms ?? 0, 1)}
            </span>
            <ArrowRight aria-hidden className='size-3 shrink-0 opacity-60' />
          </Link>
          {entries.length > 1 && (
            <p className='text-[11px] text-muted-foreground'>
              另有 {entries.length - 1} 路曾有中断
            </p>
          )}
        </>
      ) : abnormal > 0 ? (
        // Fault rows and the summary disagree (a fault older than the summary
        // window, say). The count is still true, so it is reported as a count
        // rather than dropped.
        <span className='inline-flex items-center gap-1.5 rounded-md border border-amber-500/30 bg-amber-500/10 px-2 py-1 text-xs tabular-nums text-amber-600 dark:text-amber-400'>
          <span aria-hidden className='size-1.5 rounded-full bg-amber-500' />
          {abnormal} 路有中断
        </span>
      ) : (
        <span className='inline-flex items-center gap-1.5 text-xs text-muted-foreground'>
          <span aria-hidden className='size-1.5 rounded-full bg-emerald-500' />
          无通道告警
        </span>
      )}
      {unjudged > 0 && (
        <p className='text-[11px] text-muted-foreground'>
          另有 {unjudged} 路观察不足，未纳入判断
        </p>
      )}
    </section>
  )
}

/** The span of one fault, for a tooltip: when it ran and how long. */
function faultTimeSpan(fault: Schema<'ReliabilityFault'>, zone: string): string {
  const start = formatClock(Date.parse(fault.started_at ?? ''), zone)
  const end = fault.ended_at
    ? formatClock(Date.parse(fault.ended_at), zone)
    : '持续中'
  const length = durationLabel(fault.duration_ms ?? 0, 1)
  return `${start} → ${end} · ${length}`
}

/**
 * One key/value pair in the status strip: `CPU 46%`.
 *
 * Plain text, no bar. At this width three bars plus their labels were the reason
 * the strip needed two rows and the figures ran together; the number is what an
 * operator quotes, and the bar answered "how full" for a resource whose absolute
 * value is already on the line.
 */
function ResourceStat({
  label,
  value,
  testId,
}: {
  label: string
  value: string
  testId?: string
}) {
  return (
    <span className='inline-flex items-baseline gap-1 whitespace-nowrap'>
      <span>{label}</span>
      <span
        data-testid={testId}
        className='font-mono text-foreground'
      >
        {value}
      </span>
    </span>
  )
}

/** GiB with one decimal, for the memory pair inside a single line of text. */
function gibShort(value: number): string {
  return `${(value / 1024 / 1024 / 1024).toFixed(1)}G`
}

/**
 * ComponentHealthBar is the one-line replacement for the eight-card matrix.
 *
 * The matrix repeated the operations page at four times the height, and on a
 * healthy site it was eight identical green boxes — the least informative thing
 * on the page taking the most space. Here the summary is one number and each
 * component is a pill; the detail lives in a tooltip and the full view is one
 * click away at /operations.
 *
 * An unhealthy component is sorted first and pulses, because the pills are
 * scanned rather than read and the one that needs attention must not be the
 * sixth of eight.
 */
function ComponentHealthBar({
  components,
}: {
  components: ReturnType<typeof useAPI<Component[]>>
}) {
  const list = components.data ?? []
  const enabled = list.filter((component) => component.enabled)
  const healthy = enabled.filter((component) => component.state === 'healthy')
  const unhealthyCount = enabled.length - healthy.length
  // Problems first, then healthy, then disabled: the eye lands on the left.
  const ordered = [...list].sort((a, b) => {
    const rank = (component: Component) =>
      !component.enabled ? 2 : component.state === 'healthy' ? 1 : 0
    return rank(a) - rank(b)
  })
  return (
    <section data-testid='component-health-bar' aria-label='系统组件健康' className='grid gap-2'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <p className='flex items-center gap-1.5 text-xs text-muted-foreground'>
          <Activity aria-hidden className='size-3.5' />
          站点与核心组件
        </p>
        <p className='flex items-center gap-1.5 text-xs tabular-nums'>
          <span
            aria-hidden
            className={cn(
              'size-1.5 rounded-full',
              unhealthyCount > 0 ? 'bg-red-500' : 'bg-emerald-500'
            )}
          />
          <span className={unhealthyCount > 0 ? 'text-red-500' : 'text-emerald-600 dark:text-emerald-400'}>
            {enabled.length > 0 ? `${healthy.length} / ${enabled.length} 正常` : '—'}
          </span>
        </p>
      </div>
      <QueryState
        pending={components.isPending}
        error={components.error}
        retry={() => void components.refetch()}
      />
      <ul className='flex flex-wrap items-center gap-1.5'>
        {ordered.map((component) => {
          const badge = componentBadge(component)
          const meta = COMPONENT_META[component.name] || {
            name: component.name,
            role: '',
          }
          const unhealthy = component.enabled && component.state !== 'healthy'
          return (
            <li key={component.name}>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Link
                    to='/operations'
                    data-testid='component-pill'
                    data-state={component.enabled ? component.state : 'disabled'}
                    className={cn(
                      'inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-[11px] transition-colors',
                      component.enabled
                        ? unhealthy
                          ? 'border-red-500/40 bg-red-500/10 text-red-500 hover:bg-red-500/20'
                          : 'border-border bg-muted/40 text-muted-foreground hover:bg-accent'
                        : 'border-dashed text-muted-foreground/70 hover:bg-accent'
                    )}
                  >
                    <span
                      aria-hidden
                      className={cn(
                        'size-1.5 rounded-full',
                        badge.dot,
                        unhealthy && 'animate-pulse'
                      )}
                    />
                    {meta.name}
                    {!component.enabled && ' 未启用'}
                  </Link>
                </TooltipTrigger>
                <TooltipContent side='bottom' className='max-w-xs text-xs'>
                  <p className='font-medium'>{meta.name}</p>
                  <p className='text-muted-foreground'>{meta.role}</p>
                  <p className='mt-1'>
                    {component.enabled
                      ? `${badge.label} · ${probeLabel(component.reason) || component.reason || '无详情'}`
                      : '功能未启用（部署配置关闭）'}
                  </p>
                </TooltipContent>
              </Tooltip>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

/**
 * The host's own numbers, beside the service matrix.
 *
 * Every figure renders "未知" when it was not measured. A failed read is not an
 * idle machine, so a zero-width bar or 0% would state the opposite of the truth.
 */
