import { type ReactNode, useMemo, useState } from 'react'
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
import { ReliabilityTrend } from './reliability-chart'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { QueryState, Empty } from '@/features/foundation/ui'
import {
  BAND_LEGEND,
  EXCLUSION_LABEL,
  KIND_LABEL,
  KIND_ORDER,
  WINDOW_PRESETS,
  bandReason,
  bandTone,
  faultCategoryLabel,
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
  // Which stream kind's detail is open. The three stay separate figures — REL-04
  // forbids a healthy sub stream from standing in for a broken main one — but
  // stacking all three panels meant scrolling past two of them to reach the
  // one being investigated. The summary row above always shows all three.
  const [focus, setFocus] = useState<'main' | 'sub' | 'recording'>('main')
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
  const trend = useAPI<{ items: Schema<'ReliabilityTrendPoint'>[]; precision: string }>(
    `/api/v1/channels/${channelId}/reliability/trend?${query}`,
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

      {(metrics.data?.items || []).length > 0 && (
        <KindSummary
          items={metrics.data?.items || []}
          focus={focus}
          onFocus={setFocus}
        />
      )}

      {(metrics.data?.items || [])
        .filter((item) => item.kind === focus)
        .map((item) => (
          <MetricsPanel key={item.kind} metrics={item} />
        ))}

      {(metrics.data?.items || []).length === 0 && !metrics.isPending && !metrics.error && (
        <Empty>这个窗口没有可靠性数据。</Empty>
      )}

      <TimelinePanel
        focus={focus}
        bands={(intervals.data?.items || []) as TimeBand[]}
        pending={intervals.isPending}
        error={intervals.error}
        windowEnd={new Date(window.to).getTime()}
      />

      <ReliabilityTrend
        points={trend.data?.items || []}
        pending={trend.isPending}
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
 * The three kinds side by side, as a switch and as a comparison.
 *
 * PRD REL-04 forbids merging the kinds into one number, so these are three
 * separate cards — but presenting them as one row also answers a question the
 * stacked panels could not: whether the sub stream is fine while the main one
 * is not. Clicking a card opens that kind's detail below.
 */
function KindSummary({
  items,
  focus,
  onFocus,
}: {
  items: Schema<'ReliabilityMetrics'>[]
  focus: string
  onFocus: (kind: 'main' | 'sub' | 'recording') => void
}) {
  return (
    <div className='grid gap-4 sm:grid-cols-3'>
      {KIND_ORDER.map((kind) => {
        const item = items.find((entry) => entry.kind === kind)
        const active = focus === kind
        const shown = item ? percentage(item.observed_availability) : null
        return (
          <button
            key={kind}
            type='button'
            aria-pressed={active}
            aria-label={`查看${KIND_LABEL[kind]}详情`}
            disabled={!item}
            onClick={() => onFocus(kind)}
            className={[
              'grid gap-1 rounded-lg border p-4 text-left transition-colors',
              active
                ? 'border-foreground/25 bg-muted/50'
                : 'border-border hover:bg-muted/30',
              !item && 'opacity-50',
            ]
              .filter(Boolean)
              .join(' ')}
          >
            <span className='flex items-center justify-between text-xs text-muted-foreground'>
              {KIND_LABEL[kind]}
              {!item && <span>无数据</span>}
            </span>
            {shown?.kind === 'percent' ? (
              <span
                className={[
                  'font-mono text-2xl font-bold tabular-nums',
                  item && item.observed_availability.denominator_ms > 0
                    ? toneFor(item.observed_availability)
                    : 'text-muted-foreground',
                ].join(' ')}
              >
                {shown.value}
              </span>
            ) : (
              <span className='text-sm text-muted-foreground'>
                {shown?.label ?? '无数据'}
              </span>
            )}
            {item && (
              <span className='font-mono text-xs text-muted-foreground'>
                {formatDuration(item.observed.healthy_ms)} /{' '}
                {formatDuration(item.expected_ms)}
              </span>
            )}
          </button>
        )
      })}
    </div>
  )
}

/**
 * toneFor colours a rate by what it says, not by "is it a percentage".
 *
 * Green only when time was actually available; a 0% recording rate on a channel
 * with recording switched off is not an alarm, and painting it red would train
 * the operator to ignore red.
 */
function toneFor(rate: Rate): string {
  const shown = percentage(rate)
  if (shown.kind !== 'percent') return 'text-muted-foreground'
  const value = rate.numerator_ms / rate.denominator_ms
  if (value >= 0.995) return 'text-emerald-400'
  if (value >= 0.95) return 'text-amber-400'
  return 'text-red-400'
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
  const coverage = metrics.recording_coverage
  const coveredRatio =
    coverage.denominator_ms > 0 ? coverage.numerator_ms / coverage.denominator_ms : null
  return (
    <Card data-testid={`reliability-metrics-${metrics.kind}`} className='gap-0 py-0'>
      <CardHeader className='border-b border-border/60 py-3'>
        <CardTitle className='flex items-center gap-2 text-sm font-medium'>
          {KIND_LABEL[metrics.kind] || metrics.kind}
          <span className='text-xs font-normal text-muted-foreground'>
            指标版本 {metrics.metric_version}
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent className='space-y-5 py-4'>
        {/* The three SLA rates. Each keeps its numerator and denominator: REL-04
            requires every ratio to be recomputable, and a bare percentage cannot
            be checked. */}
        <div className='grid gap-4 sm:grid-cols-3'>
          <RateCard label='源监测覆盖率' rate={metrics.monitoring_coverage} />
          <RateCard label='观测可用率' rate={metrics.observed_availability} />
          <RateCard
            label='保守可用率'
            rate={metrics.conservative_availability}
            hint='未知不当作在线，因此这是可承诺的下界'
          />
        </div>

        {/* Two 4-column data panels: time accounting, then interruptions. */}
        <div className='grid gap-4 lg:grid-cols-2'>
          <DataPanel title='时间账'>
            <DataCell label='期望拉流时长' value={formatDuration(metrics.expected_ms)} />
            <DataCell label='可用' value={formatDuration(metrics.observed.healthy_ms)} tone='ok' />
            <DataCell
              label='不可用'
              value={formatDuration(metrics.observed.unavailable_ms)}
              tone={metrics.observed.unavailable_ms > 0 ? 'bad' : undefined}
            />
            <DataCell label='未知' value={formatDuration(metrics.observed.unknown_ms)} tone='warn' />
          </DataPanel>
          <InterruptionPanel interruptions={metrics.interruptions} />
        </div>

        {/* Recording, as one bar rather than four loose durations: the question
            is whether there is a gap, and a bar answers it before the numbers. */}
        <DataPanel title='录像'>
          <div className='col-span-4 space-y-2'>
            <CoverageBar
              covered={coverage.numerator_ms}
              missing={metrics.recording_gaps.missing_ms}
              unverified={coverage.unjudgeable_ms}
              owed={coverage.denominator_ms}
            />
            <div className='grid grid-cols-2 gap-3 sm:grid-cols-4'>
              <DataCell label='应录（已结算）' value={formatDuration(coverage.denominator_ms)} />
              <DataCell label='已覆盖' value={formatDuration(coverage.numerator_ms)} tone='ok' />
              <DataCell
                label='缺口'
                value={formatDuration(metrics.recording_gaps.missing_ms)}
                tone={metrics.recording_gaps.missing_ms > 0 ? 'bad' : undefined}
              />
              <DataCell label='待核验' value={formatDuration(coverage.unjudgeable_ms)} tone='warn' />
            </div>
            {coveredRatio != null && (
              <p className='font-mono text-xs text-muted-foreground'>
                覆盖率 {((coveredRatio || 0) * 100).toFixed(2)}%
              </p>
            )}
          </div>
        </DataPanel>

        <DataPanel title='历史媒体'>
          <DataCell
            label='可访问率（已验证下界）'
            value={rateText(metrics.media_accessibility.rate)}
          />
          <DataCell
            label='未核验时长'
            value={formatDuration(metrics.media_accessibility.unverified_ms)}
          />
          <DataCell
            label='远端不可达'
            value={formatDuration(metrics.media_accessibility.remote_unknown_ms)}
          />
          <DataCell
            label='已验证覆盖'
            value={formatDuration(metrics.media_accessibility.verified_coverage_ms)}
          />
        </DataPanel>

        {metrics.observed.excluded_ms > 0 && (
          <p className='text-xs text-muted-foreground'>
            另有 {formatDuration(metrics.observed.excluded_ms)} 不要求可用（停用/未配置/维护），
            不计入任何分母。
          </p>
        )}
        {note && <p className='text-xs text-amber-600 dark:text-amber-400'>{note}</p>}
      </CardContent>
    </Card>
  )
}

type CellTone = 'ok' | 'bad' | 'warn'

const CELL_TONE: Record<CellTone, string> = {
  ok: 'text-emerald-400',
  bad: 'text-red-400',
  warn: 'text-amber-400',
}

/**
 * One SLA rate. The percentage is the headline; the evidence line under it is
 * what makes the number checkable (REL-04), so it stays visible rather than
 * moving into the tooltip with the prose.
 */
function RateCard({ label, rate, hint }: { label: string; rate: Rate; hint?: string }) {
  const shown = percentage(rate)
  return (
    <div className='min-w-0 rounded-lg border border-border bg-muted/25 p-3'>
      <p className='flex items-center gap-1.5 text-xs text-muted-foreground'>
        <span className='truncate'>{label}</span>
        {hint && <InfoTip text={hint} />}
      </p>
      {shown.kind === 'percent' ? (
        <p
          className={[
            'mt-0.5 font-mono text-2xl font-bold tabular-nums',
            toneFor(rate),
          ].join(' ')}
        >
          {shown.value}
        </p>
      ) : (
        <p className='mt-0.5 text-sm text-muted-foreground'>{shown.label}</p>
      )}
      <p className='mt-1 font-mono text-xs text-muted-foreground'>
        {rateEvidence(rate)}
      </p>
    </div>
  )
}

/** One labelled datum. Key quiet, value mono — the same pairing everywhere. */
function DataCell({
  label,
  value,
  tone,
}: {
  label: string
  value: string
  tone?: CellTone
}) {
  return (
    <div className='min-w-0'>
      <p className='truncate text-xs text-muted-foreground' title={label}>
        {label}
      </p>
      <p
        className={[
          'font-mono text-sm tabular-nums',
          tone ? CELL_TONE[tone] : 'text-foreground',
        ].join(' ')}
      >
        {value}
      </p>
    </div>
  )
}

/** A recessed read-only panel: the page's one shape for "measured facts". */
function DataPanel({
  title,
  children,
}: {
  title: string
  children: ReactNode
}) {
  return (
    <section className='rounded-lg border border-border bg-muted/25 p-4'>
      <p className='mb-3 text-xs font-medium tracking-wide text-muted-foreground'>
        {title}
      </p>
      <div className='grid grid-cols-2 gap-3 sm:grid-cols-4'>{children}</div>
    </section>
  )
}

/**
 * CoverageBar shows covered / missing / unverified as one stretch of the owed
 * time. A gap is a hole in a picture; four durations in a row make the reader
 * do that division in their head.
 */
function CoverageBar({
  covered,
  missing,
  unverified,
  owed,
}: {
  covered: number
  missing: number
  unverified: number
  owed: number
}) {
  if (owed <= 0) {
    return (
      <p className='text-xs text-muted-foreground'>
        该窗口没有应录时间（录像关闭或未配置），因此没有覆盖率。
      </p>
    )
  }
  const parts = [
    { ms: covered, tone: 'bg-emerald-500/70', label: '已覆盖' },
    { ms: missing, tone: 'bg-red-500/70', label: '缺口' },
    { ms: unverified, tone: 'bg-amber-500/60', label: '待核验' },
  ].filter((part) => part.ms > 0)
  return (
    <div
      className='flex h-3 w-full overflow-hidden rounded-full bg-muted'
      role='img'
      aria-label={parts.map((p) => `${p.label} ${formatDuration(p.ms)}`).join('，')}
    >
      {parts.map((part) => (
        <span
          key={part.label}
          title={`${part.label} ${formatDuration(part.ms)}`}
          className={part.tone}
          style={{ width: `${Math.max(0.5, (part.ms / owed) * 100)}%` }}
        />
      ))}
    </div>
  )
}

/** Interruptions, in the same 4-cell shape as the time account. */
function InterruptionPanel({
  interruptions,
}: {
  interruptions: Schema<'ReliabilityMetrics'>['interruptions']
}) {
  const mean = percentage(interruptions.mean_time_to_recovery)
  const categories = Object.entries(interruptions.by_category || {})
  if (interruptions.count === 0) {
    return (
      <section className='rounded-lg border border-border bg-muted/25 p-4'>
        <p className='mb-3 text-xs font-medium tracking-wide text-muted-foreground'>
          非计划中断
        </p>
        <p className='text-sm text-muted-foreground'>无中断事件</p>
      </section>
    )
  }
  return (
    <section className='rounded-lg border border-border bg-muted/25 p-4'>
      <p className='mb-3 text-xs font-medium tracking-wide text-muted-foreground'>
        非计划中断
      </p>
      <div className='grid grid-cols-2 gap-3 sm:grid-cols-4'>
        <DataCell label='次数' value={String(interruptions.count)} />
        <DataCell label='总时长' value={formatDuration(interruptions.total_ms)} />
        <DataCell label='最长一次' value={formatDuration(interruptions.longest_ms)} />
        <DataCell
          label='平均恢复'
          value={
            mean.kind === 'percent' && interruptions.mean_time_to_recovery.numerator_ms <= interruptions.mean_time_to_recovery.denominator_ms
              ? mean.value
              : formatDuration(interruptions.mean_time_to_recovery.numerator_ms)
          }
        />
      </div>
      {interruptions.open_count > 0 && (
        <p className='mt-2 text-xs text-red-600 dark:text-red-400'>
          持续中 {interruptions.open_count} 次 · 已 {formatDuration(interruptions.open_ms)}
        </p>
      )}
      {(interruptions.component_count > 0 || interruptions.maintenance_count > 0) && (
        <p className='mt-2 text-xs text-muted-foreground'>
          其中组件级 {interruptions.component_count} 次、计划维护{' '}
          {interruptions.maintenance_count} 次（单列，不全部归责摄像头）
        </p>
      )}
      {categories.length > 0 && (
        <p className='mt-1 text-xs text-muted-foreground'>
          归因：{categories.map(([name, count]) => `${name} ${count}`).join(' · ')}
        </p>
      )}
    </section>
  )
}

function rateText(rate: Rate): string {
  const shown = percentage(rate)
  if (shown.kind !== 'percent') return shown.label
  // A numerator larger than its denominator is not a rate; some metrics reuse the
  // Rate shape to carry a duration, and printing "33000000.00%" would be worse
  // than saying the number is not a percentage at all.
  if (rate.numerator_ms > rate.denominator_ms) {
    return formatDuration(rate.numerator_ms)
  }
  return shown.value
}

/** A label's explanation behind a hover icon, so the panel stays quiet. */
function InfoTip({ text }: { text: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          tabIndex={0}
          role='img'
          aria-label={text}
          className='inline-grid size-3.5 shrink-0 cursor-help place-items-center rounded-full border border-border text-[9px] text-muted-foreground/70 hover:text-foreground'
        >
          ?
        </span>
      </TooltipTrigger>
      <TooltipContent className='max-w-64 text-xs'>{text}</TooltipContent>
    </Tooltip>
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
  focus,
  bands,
  pending,
  error,
  windowEnd,
}: {
  focus: string
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
          KIND_ORDER.filter((kind) => kind === focus && byKind[kind]?.length).map(
            (kind) => (
              <div key={kind} className='space-y-1.5'>
                <div className='relative h-3.5 w-full overflow-hidden rounded-full bg-muted'>
                  {byKind[kind].map((band, index) => {
                    const start = new Date(band.started_at).getTime()
                    const end = band.ended_at
                      ? new Date(band.ended_at).getTime()
                      : windowEnd
                    const span = bounds.end - bounds.start
                    const left =
                      ((Math.max(start, bounds.start) - bounds.start) / span) * 100
                    const width = Math.max(0.2, ((end - start) / span) * 100)
                    return (
                      <div
                        key={`${band.started_at}-${index}`}
                        className={`absolute inset-y-0 ${bandTone(band)}`}
                        style={{ left: `${left}%`, width: `${width}%` }}
                        title={bandTitle(band, windowEnd)}
                      />
                    )
                  })}
                </div>
                <div className='flex justify-between font-mono text-[10px] text-muted-foreground'>
                  <span>{timeLabel(bounds.start)}</span>
                  <span>{timeLabel(bounds.end)}</span>
                </div>
              </div>
            )
          )}
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

/** The hover text for one band: when it ran, how long, and why. */
function bandTitle(band: TimeBand, windowEnd: number): string {
  const start = new Date(band.started_at)
  const end = band.ended_at ? new Date(band.ended_at) : new Date(windowEnd)
  const state = band.exclusion ? '不要求' : STATE_LABEL[band.state] || band.state
  const reason = bandReason(band)
  return [
    `${state}${reason ? ` · ${reason}` : ''}`,
    `${timeLabel(start.getTime())} → ${timeLabel(end.getTime())}`,
    formatDuration(end.getTime() - start.getTime()),
  ].join('\n')
}

const STATE_LABEL: Record<string, string> = {
  healthy: '可用',
  unavailable: '不可用',
  unknown: '未知',
  excluded: '不要求',
}

function timeLabel(epoch: number): string {
  return new Date(epoch).toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
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
            <CoverageBar
              covered={integrity.covered_ms}
              missing={integrity.missing_ms}
              unverified={integrity.unverified_ms}
              owed={integrity.owed_ms}
            />
            <div className='grid grid-cols-2 gap-3 sm:grid-cols-4'>
              <DataCell label='应录' value={formatDuration(integrity.owed_ms)} />
              <DataCell label='有效' value={formatDuration(integrity.covered_ms)} tone='ok' />
              <DataCell
                label='缺口'
                value={formatDuration(integrity.missing_ms)}
                tone={integrity.missing_ms > 0 ? 'bad' : undefined}
              />
              <DataCell label='待核验' value={formatDuration(integrity.unverified_ms)} tone='warn' />
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
                  <TableCell title={fault.category}>{faultCategoryLabel(fault.category)}</TableCell>
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
