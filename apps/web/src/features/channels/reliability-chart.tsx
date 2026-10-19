import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import type { Schema } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty } from '@/features/foundation/ui'
import { formatDuration } from './reliability-format'
import { trendRows, trendSpan, worthCharting } from './reliability-trend'

/**
 * Daily trend (PRD REL-05).
 *
 * Stacked hours rather than a percentage line, for the same reason the timeline
 * is a band and not a number: a day that was never observed has no percentage,
 * and plotting it as 0% would draw an outage that did not happen. Stacking keeps
 * the four kinds of time visible side by side — available, unavailable, unknown,
 * excluded — so a channel that is "down half the time" is visibly different from
 * one that was merely unmonitored.
 *
 * The colours are the timeline's own (green/red/amber/grey). Two vocabularies for
 * one fact would make the page disagree with itself.
 */
const SERIES = [
  { key: 'healthy', label: '可用', color: '#10b981' },
  { key: 'unavailable', label: '不可用', color: '#ef4444' },
  { key: 'unknown', label: '未知（证据缺口）', color: '#f59e0b' },
  { key: 'excluded', label: '不要求', color: '#71717a' },
] as const

export function ReliabilityTrend({
  points,
  pending,
}: {
  points: Schema<'ReliabilityTrendPoint'>[]
  pending: boolean
}) {
  const rows = trendRows(points)
  const span = trendSpan(rows)
  return (
    <Card data-testid='reliability-trend'>
      <CardHeader>
        <CardTitle className='text-base'>每日趋势</CardTitle>
        {span && (
          // The window is stated because a four-day trend and a four-hundred-day
          // one look identical once plotted.
          <p className='text-xs text-muted-foreground'>{span}</p>
        )}
      </CardHeader>
      <CardContent>
        {pending ? (
          <p className='text-sm text-muted-foreground'>正在加载…</p>
        ) : !worthCharting(rows) ? (
          // One point is a number, not a trend; the table above already shows it.
          <Empty>
            {rows.length === 0
              ? '该窗口还没有日汇总。汇总由区间事实推导，worker 运行一天后会开始出现。'
              : '只有一天的数据，趋势要两天以上才能看出方向。'}
          </Empty>
        ) : (
          <div className='h-64 w-full'>
            <ResponsiveContainer width='100%' height='100%'>
              <BarChart data={rows} margin={{ top: 4, right: 8, bottom: 0, left: -16 }}>
                <CartesianGrid strokeDasharray='3 3' className='stroke-border' vertical={false} />
                <XAxis dataKey='label' tick={{ fontSize: 11 }} stroke='currentColor' className='text-muted-foreground' />
                <YAxis
                  tick={{ fontSize: 11 }}
                  stroke='currentColor'
                  className='text-muted-foreground'
                  label={{ value: '小时', angle: -90, position: 'insideLeft', fontSize: 11 }}
                />
                <Tooltip
                  content={({ active, payload }) => {
                    if (!active || !payload?.length) return null
                    const row = payload[0].payload as ReturnType<typeof trendRows>[number]
                    return (
                      <div className='rounded-md border bg-popover p-2 text-xs shadow-md'>
                        <p className='mb-1 font-medium'>{row.raw}</p>
                        <p>可用 {formatDuration(row.healthyMs)}</p>
                        <p>不可用 {formatDuration(row.unavailableMs)}</p>
                        <p>未知 {formatDuration(row.unknownMs)}</p>
                        {row.faults > 0 && <p>故障 {row.faults} 次</p>}
                      </div>
                    )
                  }}
                />
                <Legend wrapperStyle={{ fontSize: 11 }} />
                {SERIES.map((series) => (
                  <Bar
                    key={series.key}
                    dataKey={series.key}
                    name={series.label}
                    stackId='time'
                    fill={series.color}
                  />
                ))}
              </BarChart>
            </ResponsiveContainer>
          </div>
        )}
        {rows.length > 0 && (
          <p className='mt-2 text-xs text-muted-foreground'>
            柱高是当天各类时长的堆叠；「未知」是监测本身缺失，不是设备故障。
          </p>
        )}
      </CardContent>
    </Card>
  )
}
