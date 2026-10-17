import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import type { Schema } from '@/lib/types'
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
import { QueryState, Empty, SelectField } from '@/features/foundation/ui'
import {
  ALERT_KIND_LABEL,
  KIND_LABEL,
  WINDOW_PRESETS,
  channelLabel,
  emptyObservationReason,
  faultCategoryLabel,
  formatDuration,
  sampleNote,
  windowFor,
} from '@/features/channels/reliability-format'
import { AlertRulesPanel } from './alert-rules'

/**
 * Cross-channel reliability comparison and the alerting rules (PRD REL-05).
 *
 * Two things this page deliberately does not do:
 *
 *  - It does not rank channels by a single score. PRD REL-05 forbids an
 *    unfounded composite "device score", and the facts here do not support one:
 *    a channel with 100% availability over ten minutes of observation is not the
 *    most reliable camera, it is the least monitored one.
 *  - It does not compute an average availability across channels. Averaging
 *    rates whose denominators differ weights a channel observed for an hour the
 *    same as one observed for a month, which describes nothing.
 *
 * So it shows the facts side by side — observed duration, availability with its
 * inputs, coverage, interruptions — and marks the rows whose evidence is too
 * thin to compare, rather than sorting them to the top.
 */
export function ReliabilityOperations() {
  const [hours, setHours] = useState<number>(24 * 7)
  const [kind, setKind] = useState<'main' | 'recording'>('main')
  const [anchor] = useState(() => windowFor(24 * 7))
  const [window, setWindow] = useState(anchor)
  const query = `from=${window.from}&to=${window.to}`

  const summary = useAPI<{ items: Schema<'ReliabilityChannelSummary'>[] }>(
    `/api/v1/reliability/summary?${query}`
  )
  const alerts = useAPI<{ items: Schema<'ReliabilityAlert'>[]; kinds: string[] }>(
    `/api/v1/reliability/alerts?${query}`
  )
  const faults = useAPI<{ items: Schema<'ReliabilityFault'>[]; categories: string[] }>(
    `/api/v1/faults?${query}`
  )

  const rows = useMemo(
    () => (summary.data?.items || []).filter((row) => row.kind === kind),
    [summary.data, kind]
  )
  const windowMs = new Date(window.to).getTime() - new Date(window.from).getTime()

  // Sorted by observed duration, not by availability: the channel with the most
  // evidence first is the only ordering that does not imply a quality ranking.
  const sorted = useMemo(
    () => [...rows].sort((a, b) => b.observed_ms - a.observed_ms),
    [rows]
  )
  const insufficient = sorted.filter((row) => !row.sample_sufficient)

  return (
    <div className='space-y-5' data-testid='reliability-operations'>
      <Card>
        <CardHeader>
          <CardTitle className='text-base'>跨通道可靠性比较</CardTitle>
        </CardHeader>
        <CardContent className='space-y-4'>
          <div className='flex flex-wrap items-end gap-3'>
            <div className='w-44'>
              <SelectField
                label='时间范围'
                value={String(hours)}
                onValueChange={(value) => {
                  const next = Number(value)
                  setHours(next)
                  setWindow(windowFor(next))
                }}
                options={WINDOW_PRESETS.map((preset) => ({
                  value: String(preset.hours),
                  label: preset.label,
                }))}
              />
            </div>
            <div className='w-36'>
              <SelectField
                label='码流'
                value={kind}
                onValueChange={(value) => setKind(value as 'main' | 'recording')}
                options={[
                  { value: 'main', label: KIND_LABEL.main },
                  { value: 'recording', label: KIND_LABEL.recording },
                ]}
              />
            </div>
            <p className='text-xs text-muted-foreground'>
              按观察时长排序，不按可用率排名：观察越少的通道不因数字好看而排前。
            </p>
          </div>

          <QueryState
            pending={summary.isPending}
            error={summary.error}
            retry={() => summary.refetch()}
          />
          {!summary.isPending && !summary.error && sorted.length === 0 && (
            <Empty>该窗口没有跨通道数据。</Empty>
          )}
          {sorted.length > 0 && (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>通道</TableHead>
                  <TableHead>观察时长</TableHead>
                  <TableHead>可用</TableHead>
                  <TableHead>不可用</TableHead>
                  <TableHead>未知</TableHead>
                  <TableHead>中断</TableHead>
                  <TableHead>结论</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sorted.map((row) => {
                  // A recording row on a site with recording off has no
                  // observation because nothing was required, not because the
                  // sample is too small. The two read very differently and only
                  // one of them is about reliability.
                  const note =
                    row.observed_ms === 0
                      ? emptyObservationReason(row.kind)
                      : sampleNote(row.observed_ms, windowMs)
                  return (
                    <TableRow key={`${row.channel_id}-${row.kind}`}>
                      <TableCell>
                        <span className='font-mono text-xs text-muted-foreground'>
                          {channelLabel(row.channel_no)}
                        </span>{' '}
                        {row.channel_name}
                      </TableCell>
                      {/* Observed duration sits beside every ratio: a channel
                          with little history cannot be read as reliable. */}
                      <TableCell className='tabular-nums'>{formatDuration(row.observed_ms)}</TableCell>
                      <TableCell className='tabular-nums text-emerald-600 dark:text-emerald-400'>
                        {formatDuration(row.healthy_ms)}
                      </TableCell>
                      <TableCell className='tabular-nums text-red-600 dark:text-red-400'>
                        {formatDuration(row.unavailable_ms)}
                      </TableCell>
                      <TableCell className='tabular-nums text-amber-600 dark:text-amber-400'>
                        {formatDuration(row.unknown_ms)}
                      </TableCell>
                      <TableCell className='tabular-nums'>{row.fault_count}</TableCell>
                      <TableCell className='text-xs'>
                        {note ? (
                          /* The full sentence is the same on every row that has
                             thin evidence, so it is shown once on hover and as
                             a short tag in the cell. Repeating it per row
                             stretched this column across the page and buried
                             the numbers it is meant to qualify. */
                          <span
                            className='whitespace-nowrap text-amber-600 dark:text-amber-400'
                            title={note}
                          >
                            证据不足
                          </span>
                        ) : (
                          <Link
                            to='/channels/configure'
                            search={{ channel: row.channel_id }}
                            className='underline'
                          >
                            查看详情
                          </Link>
                        )}
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
          {insufficient.length > 0 && (
            <p className='text-xs text-muted-foreground' data-testid='insufficient-note'>
              {insufficient.length} 个通道观察时长不足，已标注样本不足而非参与比较。
            </p>
          )}
        </CardContent>
      </Card>

      <AlertsPanel items={alerts.data?.items || []} pending={alerts.isPending} error={alerts.error} />

      <AlertRulesPanel />

      <FaultsPanel items={faults.data?.items || []} pending={faults.isPending} error={faults.error} />
    </div>
  )
}

/**
 * AlertsPanel lists threshold crossings.
 *
 * Kept apart from the fault table on purpose: a fault is what was observed, an
 * alert is what crossed a configured line. Showing them in one list would invite
 * reading an alert as a new incident.
 */
function AlertsPanel({
  items,
  pending,
  error,
}: {
  items: Schema<'ReliabilityAlert'>[]
  pending: boolean
  error: Error | null
}) {
  return (
    <Card data-testid='reliability-alerts'>
      <CardHeader>
        <CardTitle className='text-base'>阈值告警</CardTitle>
      </CardHeader>
      <CardContent className='space-y-3'>
        <p className='text-xs text-muted-foreground'>
          告警是「达到了你设的阈值」，与故障（观测到的问题）分开记录：改阈值只影响以后。
        </p>
        <QueryState pending={pending} error={error} />
        {!pending && !error && items.length === 0 && <Empty>该窗口没有阈值告警。</Empty>}
        {items.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>开始</TableHead>
                <TableHead>通道</TableHead>
                <TableHead>规则</TableHead>
                <TableHead>阈值</TableHead>
                <TableHead>实际值</TableHead>
                <TableHead>峰值</TableHead>
                <TableHead>状态</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((alert) => (
                <TableRow key={alert.id}>
                  <TableCell>{new Date(alert.started_at).toLocaleString('zh-CN', { hour12: false })}</TableCell>
                  <TableCell>{channelLabel(alert.channel_no)}</TableCell>
                  <TableCell>{ALERT_KIND_LABEL[alert.kind] || alert.kind}</TableCell>
                  <TableCell className='tabular-nums'>{alert.threshold_value}</TableCell>
                  <TableCell className='tabular-nums'>{alert.observed_value}</TableCell>
                  <TableCell className='tabular-nums'>{alert.peak_value}</TableCell>
                  <TableCell>
                    {alert.state === 'firing' ? '触发中' : '已恢复'}
                    {alert.state === 'firing' && !alert.notified && ' · 未通知'}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}

/** FaultsPanel lists faults with the effective attribution and confirmation state. */
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
    <Card data-testid='reliability-operations-faults'>
      <CardHeader>
        <CardTitle className='text-base'>
          故障
          {items.length > 0 && (
            <span className='ml-2 text-xs font-normal text-muted-foreground'>
              共 {items.length} 条，按时间倒序
            </span>
          )}
        </CardTitle>
      </CardHeader>
      <CardContent className='space-y-3'>
        <QueryState pending={pending} error={error} />
        {!pending && !error && items.length === 0 && <Empty>该窗口没有故障。</Empty>}
        {items.length > 0 && (
          /* A flapping stream can produce hundreds of rows in one window, which
             pushed every other section off the page. The list still shows all of
             them; it scrolls instead of expanding the page. */
          <div className='max-h-96 overflow-y-auto rounded-lg border'>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>开始</TableHead>
                <TableHead>通道</TableHead>
                <TableHead>码流</TableHead>
                <TableHead>归因</TableHead>
                <TableHead>时长</TableHead>
                <TableHead>状态</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((fault) => (
                <TableRow key={fault.id}>
                  <TableCell>{new Date(fault.started_at).toLocaleString('zh-CN', { hour12: false })}</TableCell>
                  <TableCell>{channelLabel(fault.channel_no)}</TableCell>
                  <TableCell>{KIND_LABEL[fault.kind] || fault.kind}</TableCell>
                  <TableCell>
                    <span title={fault.effective_category}>
                      {faultCategoryLabel(fault.effective_category)}
                    </span>
                    {fault.confirmed_category && (
                      <span className='ml-1 text-xs text-muted-foreground'>
                        （已确认，原始 {fault.initial_reason}）
                      </span>
                    )}
                  </TableCell>
                  <TableCell className='tabular-nums'>{formatDuration(fault.duration_ms || 0)}</TableCell>
                  <TableCell>
                    {fault.state === 'open' ? '持续中' : '已恢复'}
                    {fault.maintenance && ' · 计划内'}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
