import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import type { Schema, PageData } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useAPI } from '@/features/foundation/hooks'
import { QueryState, Empty } from '@/features/foundation/ui'
import {
  BAND_LEGEND,
  EXCLUSION_LABEL,
  KIND_LABEL,
  KIND_ORDER,
  WINDOW_PRESETS,
  bandReason,
  bandTone,
  formatDuration,
  percentage,
  precisionNote,
  rateEvidence,
  windowFor,
  type Rate,
  type TimeBand,
} from './reliability-format'

/**
 * The channel reliability tab (PRD REL-05).
 *
 * Three rules shape everything below, and each is a requirement rather than a
 * preference:
 *
 *  1. No figure appears without the evidence behind it. Every rate shows its
 *     numerator, denominator and unjudgeable time beside the percentage, and a
 *     rate with no value shows why instead of 0%.
 *  2. Stream kinds never merge. Main, sub and recording are separate panels, so
 *     a healthy sub stream cannot stand in for a broken main one.
 *  3. Precision is always visible when it is not exact, because a number from a
 *     summary tier means something different from one read off the observations.
 */
export function ChannelReliability({ channelId }: { channelId: string }) {
  const [hours, setHours] = useState<number>(24)
  const [custom, setCustom] = useState<{ from: string; to: string } | null>(null)
  // The window is captured when the preset changes rather than recomputed on
  // every render: a moving `to` would make each poll ask about a different
  // window, so the numbers would never settle.
  const [anchor, setAnchor] = useState(() => windowFor(24))
  const window = custom ?? anchor
  const query = `from=${window.from}&to=${window.to}`

  const metrics = useAPI<{ items: Schema<'ReliabilityMetrics'>[] }>(
    `/api/v1/channels/${channelId}/reliability/metrics?${query}`,
    !!channelId
  )
  const intervals = useAPI<PageData<Schema<'ReliabilityInterval'>>>(
    `/api/v1/channels/${channelId}/reliability/intervals?${query}`,
    !!channelId
  )
  const integrity = useAPI<Schema<'ReliabilityIntegrity'>>(
    `/api/v1/channels/${channelId}/reliability/integrity?${query}`,
    !!channelId
  )
  const faults = useAPI<PageData<Schema<'ReliabilityFault'>>>(
    `/api/v1/faults?channel_id=${channelId}&${query}`,
    !!channelId
  )

  function selectPreset(nextHours: number) {
    setHours(nextHours)
    setCustom(null)
    setAnchor(windowFor(nextHours))
  }

  return (
    <div className='space-y-5' data-testid='channel-reliability'>
      <div className='flex flex-wrap items-center gap-2'>
        {WINDOW_PRESETS.map((preset) => (
          <Button
            key={preset.hours}
            size='sm'
            variant={!custom && hours === preset.hours ? 'default' : 'outline'}
            onClick={() => selectPreset(preset.hours)}
          >
            {preset.label}
          </Button>
        ))}
        <label className='ml-2 flex items-center gap-2 text-sm text-muted-foreground'>
          自定义
          <input
            type='datetime-local'
            aria-label='开始时间'
            className='rounded-md border bg-transparent px-2 py-1 text-sm'
            onChange={(event) => {
              const value = event.target.value
              if (!value) return
              setCustom({ from: new Date(value).toISOString(), to: window.to })
            }}
          />
          —
          <input
            type='datetime-local'
            aria-label='结束时间'
            className='rounded-md border bg-transparent px-2 py-1 text-sm'
            onChange={(event) => {
              const value = event.target.value
              if (!value) return
              setCustom({ from: window.from, to: new Date(value).toISOString() })
            }}
          />
        </label>
        <a
          className='ml-auto text-sm underline'
          href={`/api/v1/channels/${channelId}/reliability/export.csv?${query}`}
        >
          导出 CSV
        </a>
      </div>

      <QueryState
        pending={metrics.isPending}
        error={metrics.error}
        retry={() => metrics.refetch()}
      />

      {(metrics.data?.items || []).map((item) => (
        <MetricsPanel key={item.kind} metrics={item} />
      ))}

      {(metrics.data?.items || []).length === 0 && !metrics.isPending && !metrics.error && (
        <Empty>这个窗口没有可靠性数据。</Empty>
      )}

      <TimelinePanel
        bands={(intervals.data?.items || []) as TimeBand[]}
        pending={intervals.isPending}
        error={intervals.error}
        windowEnd={new Date(window.to).getTime()}
      />

      <CompletenessPanel
        integrity={integrity.data}
        pending={integrity.isPending}
        error={integrity.error}
      />

      <FaultsPanel
        items={(faults.data?.items || []) as Schema<'ReliabilityFault'>[]}
        pending={faults.isPending}
        error={faults.error}
      />
    </div>
  )
}

/**
 * MetricsPanel shows one stream kind's figures.
 *
 * Deliberately one panel per kind rather than one combined table: PRD REL-04
 * forbids a healthy sub stream from masking an unavailable main stream, and the
 * surest way to honour that is to never compute a combined figure at all.
 */
function MetricsPanel({ metrics }: { metrics: Schema<'ReliabilityMetrics'> }) {
  const note = precisionNote(metrics.precision)
  return (
    <Card data-testid={`reliability-metrics-${metrics.kind}`}>
      <CardHeader>
        <CardTitle className='text-base'>
          {KIND_LABEL[metrics.kind] || metrics.kind}
        </CardTitle>
      </CardHeader>
      <CardContent className='space-y-4'>
        <div className='grid gap-3 sm:grid-cols-3'>
          <RateCard label='源监测覆盖率' rate={metrics.monitoring_coverage} />
          <RateCard label='观测可用率' rate={metrics.observed_availability} />
          <RateCard
            label='保守可用率'
            rate={metrics.conservative_availability}
            hint='未知不当作在线，因此这是可承诺的下界'
          />
        </div>

        <div className='grid gap-3 sm:grid-cols-4 text-sm'>
          <Fact label='期望拉流时长' value={formatDuration(metrics.expected_ms)} />
          <Fact label='可用' value={formatDuration(metrics.observed.healthy_ms)} />
          <Fact label='不可用' value={formatDuration(metrics.observed.unavailable_ms)} />
          <Fact label='未知' value={formatDuration(metrics.observed.unknown_ms)} />
        </div>

        <InterruptionRow interruptions={metrics.interruptions} />

        <div className='grid gap-3 sm:grid-cols-4 text-sm'>
          <Fact label='应录（已结算）' value={formatDuration(metrics.recording_coverage.denominator_ms)} />
          <Fact label='已覆盖' value={formatDuration(metrics.recording_coverage.numerator_ms)} />
          <Fact label='缺口' value={formatDuration(metrics.recording_gaps.missing_ms)} />
          <Fact label='待核验' value={formatDuration(metrics.recording_coverage.unjudgeable_ms)} />
        </div>

        <div className='grid gap-3 sm:grid-cols-2 text-sm'>
          <Fact label='历史媒体可访问率（已验证下界）' value={rateText(metrics.media_accessibility.rate)} />
          <Fact label='未核验时长' value={formatDuration(metrics.media_accessibility.unverified_ms)} />
        </div>

        {metrics.observed.excluded_ms > 0 && (
          <p className='text-sm text-muted-foreground'>
            该窗口有 {formatDuration(metrics.observed.excluded_ms)} 不要求可用（停用/未配置/维护），
            不计入任何分母。
          </p>
        )}
        {note && <p className='text-xs text-amber-600 dark:text-amber-400'>{note}</p>}
        <p className='text-xs text-muted-foreground'>指标版本 {metrics.metric_version}</p>
      </CardContent>
    </Card>
  )
}

function RateCard({ label, rate, hint }: { label: string; rate: Rate; hint?: string }) {
  const shown = percentage(rate)
  return (
    <div className='rounded-lg border p-3'>
      <p className='text-sm text-muted-foreground'>{label}</p>
      {shown.kind === 'percent' ? (
        <p className='text-2xl font-semibold tabular-nums'>{shown.value}</p>
      ) : (
        <p className='text-base text-muted-foreground'>{shown.label}</p>
      )}
      {/* REL-04: the inputs travel with every ratio so a reader can recompute it. */}
      <p className='mt-1 text-xs text-muted-foreground'>{rateEvidence(rate)}</p>
      {hint && <p className='mt-1 text-xs text-muted-foreground'>{hint}</p>}
    </div>
  )
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <p className='text-muted-foreground'>{label}</p>
      <p className='tabular-nums'>{value}</p>
    </div>
  )
}

function rateText(rate: Rate): string {
  const shown = percentage(rate)
  return shown.kind === 'percent' ? shown.value : shown.label
}

function InterruptionRow({ interruptions }: { interruptions: Schema<'ReliabilityMetrics'>['interruptions'] }) {
  const mean = percentage(interruptions.mean_time_to_recovery)
  const categories = Object.entries(interruptions.by_category || {})
  return (
    <div className='rounded-lg border p-3 text-sm'>
      <p className='text-muted-foreground'>非计划中断</p>
      <div className='mt-1 flex flex-wrap gap-x-6 gap-y-1'>
        <span className='tabular-nums'>次数 {interruptions.count}</span>
        <span className='tabular-nums'>总时长 {formatDuration(interruptions.total_ms)}</span>
        <span className='tabular-nums'>最长一次 {formatDuration(interruptions.longest_ms)}</span>
        {interruptions.open_count > 0 && (
          <span className='tabular-nums text-red-600 dark:text-red-400'>
            持续中 {interruptions.open_count} 次 · 已 {formatDuration(interruptions.open_ms)}
          </span>
        )}
        <span>平均恢复 {mean.kind === 'percent' ? mean.value : mean.label}</span>
      </div>
      {(interruptions.component_count > 0 || interruptions.maintenance_count > 0) && (
        <p className='mt-1 text-xs text-muted-foreground'>
          其中组件级 {interruptions.component_count} 次、计划维护 {interruptions.maintenance_count} 次（单列，不全部归责摄像头）
        </p>
      )}
      {categories.length > 0 && (
        <p className='mt-1 text-xs text-muted-foreground'>
          归因：{categories.map(([name, count]) => `${name} ${count}`).join(' · ')}
        </p>
      )}
    </div>
  )
}

/**
 * TimelinePanel draws availability as coloured bands, one row per stream kind.
 *
 * The canvas-free approach: a band per interval as an absolutely positioned div
 * inside a relative container. Intervals for one channel and kind never overlap,
 * so they tile the row by construction.
 */
function TimelinePanel({
  bands,
  pending,
  error,
  windowEnd,
}: {
  bands: TimeBand[]
  pending: boolean
  error: Error | null
  /** The displayed window's right edge, used as the end of open intervals. */
  windowEnd: number
}) {
  const byKind = useMemo(() => {
    const grouped: Record<string, TimeBand[]> = {}
    for (const band of bands) {
      ;(grouped[band.kind] ||= []).push(band)
    }
    return grouped
  }, [bands])

  const bounds = useMemo(() => {
    if (bands.length === 0) return null
    let start = Number.POSITIVE_INFINITY
    let end = Number.NEGATIVE_INFINITY
    for (const band of bands) {
      start = Math.min(start, new Date(band.started_at).getTime())
      // An open interval runs to the end of the displayed window, passed in
      // rather than read from the clock: the window is what the chart describes,
      // and a band that stretched to "now" would depend on render time.
      const bandEnd = band.ended_at ? new Date(band.ended_at).getTime() : windowEnd
      end = Math.max(end, bandEnd)
    }
    if (!Number.isFinite(start) || end <= start) return null
    return { start, end }
  }, [bands, windowEnd])

  return (
    <Card data-testid='reliability-timeline'>
      <CardHeader>
        <CardTitle className='text-base'>可用性时间带</CardTitle>
      </CardHeader>
      <CardContent className='space-y-3'>
        <QueryState pending={pending} error={error} />
        {!pending && !error && bands.length === 0 && <Empty>该窗口没有状态记录。</Empty>}
        {bounds &&
          KIND_ORDER.filter((kind) => byKind[kind]?.length).map((kind) => (
            <div key={kind} className='space-y-1'>
              <p className='text-sm text-muted-foreground'>{KIND_LABEL[kind]}</p>
              <div className='relative h-6 w-full overflow-hidden rounded bg-muted'>
                {byKind[kind].map((band, index) => {
                  const start = new Date(band.started_at).getTime()
                  const end = band.ended_at ? new Date(band.ended_at).getTime() : windowEnd
                  const span = bounds.end - bounds.start
                  const left = ((Math.max(start, bounds.start) - bounds.start) / span) * 100
                  const width = Math.max(0.2, ((end - start) / span) * 100)
                  return (
                    <div
                      key={`${band.started_at}-${index}`}
                      className={`absolute inset-y-0 ${bandTone(band)}`}
                      style={{ left: `${left}%`, width: `${width}%` }}
                      title={`${band.state} · ${bandReason(band)}`}
                    />
                  )
                })}
              </div>
            </div>
          ))}
        {bands.length > 0 && (
          <div className='flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground'>
            {BAND_LEGEND.map((entry) => (
              <span key={entry.label} className='flex items-center gap-1'>
                <span className={`inline-block h-2.5 w-2.5 rounded-sm ${entry.tone}`} />
                {entry.label}
              </span>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

/**
 * CompletenessPanel shows the recording owed/covered/gap split.
 *
 * The four durations sum to the owed time by construction, and excluded time
 * appears separately: "not required to record" is a different fact from
 * "required and missing".
 */
function CompletenessPanel({
  integrity,
  pending,
  error,
}: {
  integrity?: Schema<'ReliabilityIntegrity'>
  pending: boolean
  error: Error | null
}) {
  const exclusions = Object.entries(integrity?.exclusions || {})
  const precision = precisionNote(integrity?.precision)
  return (
    <Card data-testid='reliability-completeness'>
      <CardHeader>
        <CardTitle className='text-base'>录像完整性</CardTitle>
      </CardHeader>
      <CardContent className='space-y-3 text-sm'>
        <QueryState pending={pending} error={error} />
        {integrity && (
          <>
            <div className='grid gap-3 sm:grid-cols-4'>
              <Fact label='应录' value={formatDuration(integrity.owed_ms)} />
              <Fact label='有效' value={formatDuration(integrity.covered_ms)} />
              <Fact label='缺口' value={formatDuration(integrity.missing_ms)} />
              <Fact label='待核验' value={formatDuration(integrity.unverified_ms)} />
            </div>
            {exclusions.length > 0 && (
              <p className='text-xs text-muted-foreground'>
                不要求录像：
                {exclusions
                  .map(([reason, ms]) => `${EXCLUSION_LABEL[reason] || reason} ${formatDuration(ms)}`)
                  .join(' · ')}
              </p>
            )}
            {precision && <p className='text-xs text-amber-600 dark:text-amber-400'>{precision}</p>}
          </>
        )}
      </CardContent>
    </Card>
  )
}

/**
 * FaultsPanel lists this channel's faults with a link to the operations page.
 *
 * Not a detail view: the faults table already has one, and duplicating it here
 * would give two places to fix when the presentation changes.
 */
function FaultsPanel({
  items,
  pending,
  error,
}: {
  items: Schema<'ReliabilityFault'>[]
  pending: boolean
  error: Error | null
}) {
  return (
    <Card data-testid='reliability-faults'>
      <CardHeader>
        <CardTitle className='text-base'>故障</CardTitle>
      </CardHeader>
      <CardContent>
        <QueryState pending={pending} error={error} />
        {!pending && !error && items.length === 0 && <Empty>该窗口没有故障记录。</Empty>}
        {items.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>开始</TableHead>
                <TableHead>码流</TableHead>
                <TableHead>分类</TableHead>
                <TableHead>时长</TableHead>
                <TableHead>状态</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((fault) => (
                <TableRow key={fault.id}>
                  <TableCell>{new Date(fault.started_at).toLocaleString('zh-CN', { hour12: false })}</TableCell>
                  <TableCell>{KIND_LABEL[fault.kind] || fault.kind}</TableCell>
                  <TableCell>{fault.category}</TableCell>
                  <TableCell className='tabular-nums'>{formatDuration(fault.duration_ms || 0)}</TableCell>
                  <TableCell>
                    {fault.state === 'open' ? '持续中' : '已恢复'}
                    {fault.maintenance && ' · 计划内'}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        <div className='mt-3'>
          <Link to='/operations' className='text-sm underline'>
            在运维页查看与确认故障
          </Link>
        </div>
      </CardContent>
    </Card>
  )
}
