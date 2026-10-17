import { useState } from 'react'
import type { Schema } from '@/lib/types'
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
import { useAPI, useAction } from '@/features/foundation/hooks'
import { QueryState, Empty, Field, SelectField, Notices } from '@/features/foundation/ui'
import { apiRequest } from '@/lib/api-client'
import { ALERT_KIND_LABEL } from '@/features/channels/reliability-format'

/**
 * Alert threshold configuration (PRD REL-05).
 *
 * Four rules, and each needs a different kind of number: three are durations and
 * one is a coverage floor. The form asks for the unit the kind actually uses
 * rather than a raw millisecond count, because a threshold an operator cannot
 * read back is a threshold they will set wrong.
 *
 * Nothing is enabled by default. The PRD says the thresholds are configurable,
 * not that any particular one is on, and a release that starts alerting on its
 * own gets muted within a week.
 */
const DURATION_KINDS = [
  { value: 'main_down_streak', unit: 'minute' as const },
  { value: 'recording_segment_timeout', unit: 'minute' as const },
  { value: 'monitoring_stopped', unit: 'minute' as const },
]

export function AlertRulesPanel() {
  const rules = useAPI<{ items: Schema<'ReliabilityAlertRule'>[]; kinds: string[] }>(
    '/api/v1/reliability/alert-rules'
  )
  const channels = useAPI<{ items: Schema<'ChannelSummary'>[] }>('/api/v1/channels/summary')
  const [kind, setKind] = useState('main_down_streak')
  const [channel, setChannel] = useState('')
  const [value, setValue] = useState('10')
  const action = useAction(true)

  const isDuration = DURATION_KINDS.some((entry) => entry.value === kind)

  async function save() {
    const numeric = Number(value)
    if (!Number.isFinite(numeric) || numeric <= 0) return
    const body = isDuration
      ? { kind, channel_id: channel || undefined, threshold_ms: Math.round(numeric * 60_000) }
      : { kind, channel_id: channel || undefined, threshold_percent: numeric }
    await action.run(
      () =>
        apiRequest('/api/v1/reliability/alert-rules', {
          method: 'POST',
          body: JSON.stringify(body),
        }),
      '阈值已保存',
      true
    )
    await rules.refetch()
  }

  async function remove(id: string) {
    await action.run(
      () => apiRequest(`/api/v1/reliability/alert-rules/${id}`, { method: 'DELETE' }),
      '规则已删除',
      true
    )
    await rules.refetch()
  }

  return (
    <Card data-testid='alert-rules'>
      <CardHeader>
        <CardTitle className='text-base'>告警阈值</CardTitle>
      </CardHeader>
      <CardContent className='space-y-4'>
        <p className='text-xs text-muted-foreground'>
          默认不启用任何阈值：不配置就不会报警。持续越限只产生一条告警（刷新取值），
          恢复后保留记录。
        </p>
        <Notices error={action.error} notice={action.notice} />

        <div className='flex flex-wrap items-end gap-3'>
          <div className='w-52'>
            <SelectField
              label='规则'
              value={kind}
              onValueChange={setKind}
              options={Object.entries(ALERT_KIND_LABEL).map(([value, label]) => ({ value, label }))}
            />
          </div>
          <div className='w-52'>
            <SelectField
              label='通道'
              value={channel}
              onValueChange={setChannel}
              options={[
                { value: '', label: '全站（所有通道）' },
                ...(channels.data?.items || []).map((item) => ({
                  value: item.channel_id,
                  label: `CH${String(item.channel_no).padStart(2, '0')} ${item.channel_name}`,
                })),
              ]}
            />
          </div>
          <div className='w-40'>
            <Field
              label={isDuration ? '阈值（分钟）' : '阈值（覆盖率 %）'}
              name='threshold'
              type='number'
              min='0'
              step={isDuration ? '1' : '0.5'}
              value={value}
              onChange={(event) => setValue(event.target.value)}
            />
          </div>
          <Button disabled={action.pending} onClick={() => void save()}>
            保存阈值
          </Button>
        </div>

        <QueryState
          pending={rules.isPending}
          error={rules.error}
          retry={() => rules.refetch()}
        />
        {!rules.isPending && !rules.error && (rules.data?.items || []).length === 0 && (
          <Empty>还没有配置任何阈值。</Empty>
        )}
        {(rules.data?.items || []).length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>规则</TableHead>
                <TableHead>范围</TableHead>
                <TableHead>阈值</TableHead>
                <TableHead>状态</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {(rules.data?.items || []).map((rule) => {
                const channelRow = (channels.data?.items || []).find(
                  (item) => item.channel_id === rule.channel_id
                )
                return (
                  <TableRow key={rule.id}>
                    <TableCell>{ALERT_KIND_LABEL[rule.kind] || rule.kind}</TableCell>
                    <TableCell>
                      {rule.channel_id
                        ? `CH${String(channelRow?.channel_no ?? '').padStart(2, '0')} ${channelRow?.channel_name ?? ''}`
                        : '全站'}
                    </TableCell>
                    <TableCell className='tabular-nums'>
                      {rule.threshold_ms != null
                        ? `${Math.round(rule.threshold_ms / 60_000)} 分钟`
                        : `${rule.threshold_percent ?? 0}%`}
                    </TableCell>
                    <TableCell>{rule.enabled ? '启用' : '停用'}</TableCell>
                    <TableCell>
                      <Button
                        variant='outline'
                        size='sm'
                        aria-label={`删除 ${ALERT_KIND_LABEL[rule.kind] || rule.kind}`}
                        onClick={() => void remove(rule.id)}
                      >
                        删除
                      </Button>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}
