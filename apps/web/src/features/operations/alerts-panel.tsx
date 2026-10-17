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
import { QueryState, Empty, Field, SelectField, Notices } from '@/features/foundation/ui'
import { useAPI, useAction } from '@/features/foundation/hooks'
import { apiRequest } from '@/lib/api-client'
import {
  ALERT_KIND_LABEL,
  channelLabel,
} from '@/features/channels/reliability-format'
import { InfoHint } from './info-hint'
import { RelativeTime } from './relative-time'

/**
 * Threshold alerts and the rules that produce them, in one card.
 *
 * They were two cards because they are two different records — an alert is what
 * crossed a line, a rule is where the line is — and that distinction is kept in
 * the layout: alerts on top, configuration below, with a rule's effect never
 * redrawing an existing alert. What changed is only the arrangement: reading
 * "the threshold fired" and "there is no threshold configured" in two separate
 * boxes made the empty state look like a fault rather than a setting.
 */

const DURATION_KINDS = [
  { value: 'main_down_streak', unit: 'minute' as const },
  { value: 'recording_segment_timeout', unit: 'minute' as const },
  { value: 'monitoring_stopped', unit: 'minute' as const },
]

export function AlertAndRulesPanel({
  items,
  pending,
  error,
}: {
  items: Schema<'ReliabilityAlert'>[]
  pending: boolean
  error: Error | null
}) {
  const [showRules, setShowRules] = useState(false)
  const firing = items.filter((item) => item.state === 'firing').length
  return (
    <Card id='alerts' data-testid='reliability-alerts' className='scroll-mt-24'>
      <CardHeader>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <CardTitle className='flex items-center gap-2 text-base'>
            阈值告警与规则
            <InfoHint label='告警与规则的区别'>
              告警是「达到了你设的阈值」，由配置产生；故障是「观测到的问题」，由采样产生。
              改阈值只影响以后的判定，不会重写已记录的告警或故障。
              默认不启用任何阈值：不配置就不会报警；持续越限只产生一条告警（刷新取值），恢复后保留记录。
            </InfoHint>
            {firing > 0 && (
              <span className='flex items-center gap-1.5 rounded-full border border-red-500/20 bg-red-500/10 px-2 py-0.5 text-xs font-normal text-red-400'>
                <span aria-hidden className='size-1.5 rounded-full bg-red-500' />
                {firing} 条触发中
              </span>
            )}
          </CardTitle>
          <Button
            type='button'
            variant='outline'
            size='sm'
            aria-expanded={showRules}
            onClick={() => setShowRules((value) => !value)}
          >
            {showRules ? '收起规则' : '配置规则'}
          </Button>
        </div>
      </CardHeader>
      <CardContent className='space-y-4'>
        <section className='space-y-3'>
          <h3 className='text-sm font-medium'>当前告警</h3>
          <QueryState pending={pending} error={error} />
          {!pending && !error && items.length === 0 && (
            <Empty>该窗口没有阈值告警。</Empty>
          )}
          {items.length > 0 && (
            <div className='overflow-x-auto rounded-lg border'>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>开始</TableHead>
                    <TableHead>通道</TableHead>
                    <TableHead>规则</TableHead>
                    <TableHead className='text-right'>阈值</TableHead>
                    <TableHead className='text-right'>实际值</TableHead>
                    <TableHead className='text-right'>峰值</TableHead>
                    <TableHead>状态</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {items.map((alert) => (
                    <TableRow key={alert.id}>
                      <TableCell className='whitespace-nowrap'>
                        <RelativeTime value={alert.started_at} />
                      </TableCell>
                      <TableCell className='whitespace-nowrap'>
                        {channelLabel(alert.channel_no)}
                      </TableCell>
                      <TableCell>
                        {ALERT_KIND_LABEL[alert.kind] || alert.kind}
                      </TableCell>
                      <TableCell className='tabular-nums text-right'>
                        {alert.threshold_value}
                      </TableCell>
                      <TableCell className='tabular-nums text-right'>
                        {alert.observed_value}
                      </TableCell>
                      <TableCell className='tabular-nums text-right'>
                        {alert.peak_value}
                      </TableCell>
                      <TableCell className='whitespace-nowrap'>
                        {alert.state === 'firing' ? (
                          <span className='flex items-center gap-1.5 text-red-400'>
                            <span
                              aria-hidden
                              className='size-1.5 rounded-full bg-red-500'
                            />
                            触发中
                          </span>
                        ) : (
                          <span className='text-muted-foreground'>已恢复</span>
                        )}
                        {alert.state === 'firing' && !alert.notified && (
                          <span className='ml-1 text-xs text-amber-500'>
                            · 未通知
                          </span>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </section>

        {showRules && (
          <>
            <div className='border-t' />
            <AlertRulesForm />
          </>
        )}
      </CardContent>
    </Card>
  )
}

/** AlertRulesForm is the configuration half: the form and the rules in effect. */
function AlertRulesForm() {
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

  async function remove(ruleID: string) {
    await action.run(
      () => apiRequest(`/api/v1/reliability/alert-rules/${ruleID}`, { method: 'DELETE' }),
      '规则已删除',
      true
    )
    await rules.refetch()
  }

  const items = rules.data?.items || []
  return (
    <section className='space-y-4' data-testid='alert-rules'>
      <h3 className='text-sm font-medium'>告警规则</h3>
      <Notices error={action.error} notice={action.notice} />
      <div className='flex flex-wrap items-end gap-3'>
        <div className='w-52'>
          <SelectField
            label='规则'
            value={kind}
            onValueChange={setKind}
            options={Object.entries(ALERT_KIND_LABEL).map(([key, label]) => ({
              value: key,
              label,
            }))}
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
                label: channelLabel(item.channel_no) + ' ' + item.channel_name,
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
      {!rules.isPending && !rules.error && items.length === 0 && (
        <Empty>还没有配置任何阈值。</Empty>
      )}
      {items.length > 0 && (
        <div className='overflow-x-auto rounded-lg border'>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>规则</TableHead>
                <TableHead>范围</TableHead>
                <TableHead className='text-right'>阈值</TableHead>
                <TableHead>状态</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((rule) => {
                const channelRow = (channels.data?.items || []).find(
                  (item) => item.channel_id === rule.channel_id
                )
                return (
                  <TableRow key={rule.id}>
                    <TableCell>
                      {ALERT_KIND_LABEL[rule.kind] || rule.kind}
                    </TableCell>
                    <TableCell className='whitespace-nowrap'>
                      {rule.channel_id
                        ? channelLabel(channelRow?.channel_no) +
                          ' ' +
                          (channelRow?.channel_name ?? '')
                        : '全站'}
                    </TableCell>
                    <TableCell className='tabular-nums text-right'>
                      {rule.threshold_ms != null
                        ? `${Math.round(rule.threshold_ms / 60_000)} 分钟`
                        : `${rule.threshold_percent ?? 0}%`}
                    </TableCell>
                    <TableCell>
                      {rule.enabled ? '启用' : '停用'}
                    </TableCell>
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
        </div>
      )}
    </section>
  )
}
