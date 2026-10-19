import { Fragment, useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Switch } from '@/components/ui/switch'
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
  KIND_LABEL,
  WINDOW_PRESETS,
  channelLabel,
  emptyObservationReason,
  faultCategoryLabel,
  formatDuration,
  sampleNote,
  windowFor,
} from '@/features/channels/reliability-format'
import { AlertAndRulesPanel } from './alerts-panel'
import { InfoHint } from './info-hint'
import {
  DURATION_FILTERS,
  EMPTY_FILTERS,
  MINIMUM_SAMPLE_MS,
  SEVERE_FAULT_MS,
  slicePage,
  filterFaults,
  groupFlappingFaults,
  groupIsSevere,
  type FaultGroup,
  isSevere,
  orderFaults,
  type FaultFilters,
  type FaultStateFilter,
} from './operations-format'
import { Pager } from './pager'
import { RelativeTime } from './relative-time'
import { Sparkbar } from './sparkbar'

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
    <div className='flex flex-col gap-5' data-testid='reliability-operations'>
      <Card id='reliability' className='scroll-mt-24'>
        <CardHeader>
          <CardTitle className='flex items-center gap-2 text-base'>
            跨通道可靠性比较
            <InfoHint label='为什么按观察时长排序'>
              按观察时长排序，不按可用率排名：窗口内只观测十分钟的通道不是最可靠，
              而是被监测得最少。样本不足的通道会标注而不是参与比较，也不给出跨通道平均可用率
              —— 平均不同分母的比率会把观测一小时的通道和观测一个月的通道等权。
            </InfoHint>
          </CardTitle>
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
                  <TableHead>
                    <span className='flex items-center gap-1'>
                      结论
                      <InfoHint label='证据不足的判定基准'>
                        有效评估至少需要 {formatDuration(MINIMUM_SAMPLE_MS)} 的观测：
                        这是绝对时长而非窗口占比，因此七天的窗口里观测 14 小时不会被判为样本不足。
                        低于该时长的行只标注「证据不足」，不参与可靠性比较。
                      </InfoHint>
                    </span>
                  </TableHead>
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
                      <TableCell className='min-w-32'>
                        <span className='tabular-nums'>
                          {formatDuration(row.observed_ms)}
                        </span>
                        {/* The bar makes an anomalous row findable without
                            reading three numbers: a thin red sliver among solid
                            green rows is the outlier. */}
                        <div className='mt-1'>
                          <Sparkbar
                            healthyMs={row.healthy_ms}
                            unavailableMs={row.unavailable_ms}
                            unknownMs={row.unknown_ms}
                          />
                        </div>
                      </TableCell>
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
                            search={{ channel: row.channel_id, tab: '' }}
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

      <AlertAndRulesPanel
        items={alerts.data?.items || []}
        pending={alerts.isPending}
        error={alerts.error}
      />

      <FaultsPanel items={faults.data?.items || []} pending={faults.isPending} error={faults.error} />
    </div>
  )
}


/**
 * FaultsPanel lists faults, filterable and with flapping aggregated.
 *
 * The problem this solves is signal-to-noise: one flapping sub stream produced
 * hundreds of ten-second entries, and a nine-minute main-stream outage from the
 * same morning was buried among them. Everything here is frontend state over the
 * one list the API already returned — filtering and grouping change what is
 * shown, never what was recorded, and the count always reports the filtered
 * total so a narrowed view cannot be mistaken for a quiet window.
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
  const [filters, setFilters] = useState<FaultFilters>(EMPTY_FILTERS)
  const [aggregate, setAggregate] = useState(true)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [page, setPage] = useState(1)

  // Channel options come from the faults themselves rather than a channels call:
  // offering a channel that has no faults in this window would produce an empty
  // table and no explanation.
  const channelOptions = useMemo(() => {
    const seen = new Map<string, { no: number }>()
    for (const fault of items) {
      if (!seen.has(fault.channel_id)) {
        // The fault list carries the channel number but not its name; the
        // number is what the table and the operator use.
        seen.set(fault.channel_id, { no: fault.channel_no })
      }
    }
    return [...seen.entries()]
      .map(([id, value]) => ({ id, ...value }))
      .sort((a, b) => a.no - b.no)
  }, [items])

  const filtered = useMemo(() => filterFaults(items, filters), [items, filters])
  const groups = useMemo(
    () => (aggregate ? groupFlappingFaults(filtered) : null),
    [filtered, aggregate]
  )
  // One entry per row to render: a group when aggregating, a bare fault
  // otherwise. Carrying the group alongside the fault avoids looking it up by
  // id later, which would mis-associate if two rows shared an id.
  interface FaultRow {
    key: string
    fault: Schema<'ReliabilityFault'>
    group: FaultGroup | null
  }
  const rows = useMemo<FaultRow[]>(() => {
    if (groups) return groups.map((group) => ({ key: group.key, fault: group.latest, group }))
    return orderFaults(filtered).map((fault) => ({ key: fault.id, fault, group: null }))
  }, [groups, filtered])
  // Counted per group by its longest single outage, not by the summed duration:
  // twelve ten-second blips are a flapping stream, one nine-minute outage is a
  // different failure. The badge says how many rows contain a real outage.
  const severe = groups
    ? groups.filter((group) => groupIsSevere(group)).length
    : filtered.filter((fault) => isSevere(fault.duration_ms)).length
  const open = filtered.filter((fault) => fault.state === 'open').length
  // Ten visible rows per page, the same rhythm as the audit list. The table used
  // to scroll inside a 384px box, which fought the page scroll and hid how much
  // history existed; a pager shows both.
  const paged = slicePage(rows, page)
  const visibleRows = paged.rows
  const filteredActive =
    filters.channel !== '' ||
    filters.kind !== '' ||
    filters.minDurationMs > 0 ||
    filters.state !== 'all'

  return (
    <Card id='faults' data-testid='reliability-operations-faults' className='scroll-mt-24'>
      <CardHeader>
        <div className='flex flex-wrap items-center justify-between gap-3'>
          <CardTitle className='flex flex-wrap items-center gap-2 text-base'>
            故障
            <span className='text-xs font-normal text-muted-foreground'>
              {filtered.length === items.length
                ? `共 ${items.length} 条`
                : `${filtered.length} / ${items.length} 条`}
              {groups && filtered.length > 0 && ` · ${groups.length} 组`}
            </span>
            {open > 0 && (
              <span className='flex items-center gap-1.5 rounded-full border border-red-500/20 bg-red-500/10 px-2 py-0.5 text-xs font-normal text-red-400'>
                <span aria-hidden className='size-1.5 rounded-full bg-red-500' />
                {open} 条未恢复
              </span>
            )}
            {severe > 0 && (
              <button
                type='button'
                aria-pressed={filters.minDurationMs >= SEVERE_FAULT_MS}
                onClick={() =>
                  setFilters({
                    ...filters,
                    // Toggling off restores "all durations" rather than some
                    // intermediate floor the operator did not ask for.
                    minDurationMs:
                      filters.minDurationMs >= SEVERE_FAULT_MS
                        ? 0
                        : SEVERE_FAULT_MS,
                  })
                }
                title='只看单次 ≥ 5 分钟的故障'
                className='cursor-pointer rounded-full border border-amber-500/20 bg-amber-500/10 px-2 py-0.5 text-xs font-normal text-amber-400 transition-colors hover:bg-amber-500/20 aria-pressed:ring-1 aria-pressed:ring-amber-500/40'
              >
                {severe} 条 ≥ 5 分钟
              </button>
            )}
            <InfoHint label='故障筛选与聚合说明'>
              筛选与聚合只改变展示，不修改记录。同一通道、同一码流、同一归因且相邻时间内反复中断的条目会归为一行；
              「≥ 5 分钟」只按单次中断判定，多次短抖动的累加不算。
            </InfoHint>
          </CardTitle>
          <div className='flex flex-wrap items-center gap-2'>
            <SelectField
              ariaLabel='通道'
              value={filters.channel}
              onValueChange={(value) => setFilters({ ...filters, channel: value })}
              className='w-36'
              options={[
                { value: '', label: '全部通道' },
                ...channelOptions.map((option) => ({
                  value: option.id,
                  label: channelLabel(option.no),
                })),
              ]}
            />
            <SelectField
              ariaLabel='码流'
              value={filters.kind}
              onValueChange={(value) => setFilters({ ...filters, kind: value })}
              className='w-28'
              options={[
                { value: '', label: '全部码流' },
                { value: 'main', label: KIND_LABEL.main },
                { value: 'sub', label: KIND_LABEL.sub },
              ]}
            />
            <SelectField
              ariaLabel='最短时长'
              value={String(filters.minDurationMs)}
              onValueChange={(value) =>
                setFilters({ ...filters, minDurationMs: Number(value) })
              }
              className='w-32'
              options={DURATION_FILTERS.map((option) => ({
                value: option.value,
                label: option.value === '0' ? '全部时长' : option.label,
              }))}
            />
            <SelectField
              ariaLabel='状态'
              value={filters.state}
              onValueChange={(value) =>
                setFilters({ ...filters, state: value as FaultStateFilter })
              }
              className='w-28'
              options={[
                { value: 'all', label: '全部状态' },
                { value: 'open', label: '未恢复' },
                { value: 'resolved', label: '已恢复' },
              ]}
            />
            <label className='flex items-center gap-2 text-xs text-muted-foreground'>
              <Switch
                checked={aggregate}
                onCheckedChange={setAggregate}
                aria-label='聚合连续抖动'
              />
              聚合连续抖动
            </label>
            {filteredActive && (
              <Button
                variant='ghost'
                size='sm'
                onClick={() => setFilters(EMPTY_FILTERS)}
              >
                清除
              </Button>
            )}
          </div>
        </div>
      </CardHeader>
      <CardContent className='space-y-3'>
        <QueryState pending={pending} error={error} />
        {!pending && !error && items.length === 0 && <Empty>该窗口没有故障。</Empty>}
        {!pending && !error && items.length > 0 && filtered.length === 0 && (
          <Empty>当前筛选条件下没有故障。</Empty>
        )}
        {rows.length > 0 && (
          <div className='overflow-x-auto rounded-lg border'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>开始</TableHead>
                  <TableHead>通道</TableHead>
                  <TableHead>码流</TableHead>
                  <TableHead>归因</TableHead>
                  <TableHead className='text-right'>时长</TableHead>
                  <TableHead>状态</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visibleRows.map(({ key, fault, group }) => {
                  const isOpen = group?.open || fault.state === 'open'
                  const duration = group?.totalDurationMs ?? fault.duration_ms ?? 0
                  const isExpanded = group ? expanded.has(group.key) : false
                  return (
                    <Fragment key={key}>
                      <TableRow
                        className={isOpen ? 'bg-red-500/5' : undefined}
                      >
                        <TableCell className='whitespace-nowrap'>
                          <RelativeTime value={fault.started_at} />
                        </TableCell>
                        <TableCell className='whitespace-nowrap'>
                          {channelLabel(fault.channel_no)}
                        </TableCell>
                        <TableCell>{KIND_LABEL[fault.kind] || fault.kind}</TableCell>
                        <TableCell>
                          <span title={fault.effective_category}>
                            {faultCategoryLabel(fault.effective_category)}
                          </span>
                          {fault.confirmed_category && (
                            <span className='ml-1 text-xs text-muted-foreground'>
                              （已确认）
                            </span>
                          )}
                        </TableCell>
                        <TableCell className='whitespace-nowrap pr-6 text-right'>
                          {/* Highlight only a single outage of five minutes or
                              more. A group whose members add up past five
                              minutes is a flapping stream and reads as a
                              cumulative figure instead — the same colour for
                              both would hide the distinction that matters. */}
                          <span
                            className={
                              isSevere(fault.duration_ms)
                                ? 'font-semibold tabular-nums text-amber-500'
                                : 'tabular-nums text-muted-foreground'
                            }
                          >
                            {group && group.count > 1
                              ? `累计 ${formatDuration(duration)} (${group.count} 次)`
                              : formatDuration(duration)}
                          </span>
                        </TableCell>
                        <TableCell className='whitespace-nowrap'>
                          {isOpen ? (
                            <span className='flex items-center gap-1.5 text-red-400'>
                              <span
                                aria-hidden
                                className='size-1.5 rounded-full bg-red-500'
                              />
                              持续中
                            </span>
                          ) : (
                            <span className='text-muted-foreground'>已恢复</span>
                          )}
                          {fault.maintenance && (
                            <span className='ml-1 text-xs text-muted-foreground'>
                              · 计划内
                            </span>
                          )}
                          {group && group.count > 1 && (
                            <button
                              type='button'
                              className='ml-2 text-xs underline'
                              aria-expanded={isExpanded}
                              onClick={() =>
                                setExpanded((current) => {
                                  const next = new Set(current)
                                  if (next.has(group.key)) next.delete(group.key)
                                  else next.add(group.key)
                                  return next
                                })
                              }
                            >
                              {isExpanded ? '收起明细' : '展开明细'}
                            </button>
                          )}
                        </TableCell>
                      </TableRow>
                      {group && isExpanded &&
                        group.items.slice(1).map((member) => (
                          <TableRow
                            key={member.id}
                            className='bg-muted/30 text-xs'
                          >
                            <TableCell className='whitespace-nowrap pl-8'>
                              <RelativeTime value={member.started_at} />
                            </TableCell>
                            <TableCell colSpan={2} className='text-muted-foreground'>
                              同一组内的独立中断
                            </TableCell>
                            <TableCell className='text-muted-foreground'>
                              {faultCategoryLabel(member.effective_category)}
                            </TableCell>
                            <TableCell className='whitespace-nowrap text-right tabular-nums text-muted-foreground'>
                              {formatDuration(member.duration_ms ?? 0)}
                            </TableCell>
                            <TableCell className='text-muted-foreground'>
                              {member.state === 'open' ? '持续中' : '已恢复'}
                            </TableCell>
                          </TableRow>
                        ))}
                    </Fragment>
                  )
                })}
              </TableBody>
            </Table>
          </div>
        )}
        {rows.length > 0 && (
          <Pager
            page={paged.page}
            pageCount={paged.pageCount}
            rowCount={rows.length}
            onPage={(next) => {
              setPage(next)
              // Collapsed detail belongs to the page it was opened on; carrying
              // it across pages would expand a row the reader never opened.
              setExpanded(new Set())
            }}
            note={
              groups && filtered.length > rows.length
                ? `${filtered.length} 条故障合并为 ${rows.length} 行`
                : undefined
            }
          />
        )}
      </CardContent>
    </Card>
  )
}
