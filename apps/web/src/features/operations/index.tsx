import { useState } from 'react'
import { useAuthStore } from '@/stores/auth-store'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { StatusDot } from '@/features/foundation/status-badge'
import { useAPI, fields, text } from '@/features/foundation/hooks'
import {
  PageTitle,
  Panel,
  Field,
  QueryState,
  Forbidden,
} from '@/features/foundation/ui'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { AbnormalEvents } from './abnormal-events'
import { ReliabilityOperations } from './reliability'
import { AuditFamilyFilter, AuditTrail } from './audit-trail'

/**
 * In-page anchors, in the order the sections appear.
 *
 * The page is five blocks tall and an operator usually arrives looking for one of
 * them — "is the worker up", "what broke overnight", "who deleted those
 * recordings". Scrolling past four unrelated cards to reach the fifth is the
 * tax this removes.
 */
const ANCHORS = [
  { id: 'abnormal', label: '异常事件' },
  { id: 'components', label: '组件状态' },
  { id: 'reliability', label: '可靠性' },
  { id: 'alerts', label: '告警' },
  { id: 'faults', label: '故障' },
  { id: 'audit', label: '操作审计' },
]

/**
 * Component names and the reason codes the prober writes.
 *
 * The page used to print the raw pair ("healthy · probe_succeeded"), which is the
 * diagnostic vocabulary of the code, not of the operator reading it. The codes
 * stay underneath for support; the label is what the eye should land on.
 */
const COMPONENT_LABEL: Record<string, string> = {
  gateway: '接入网关',
  api: 'API 服务',
  worker: '后台任务',
  postgres: '数据库',
  zlm: '流媒体服务',
  frigate: '智能检测器',
  mqtt: '消息队列',
  openlist: '自带云盘',
}

const STATE_LABEL: Record<string, string> = {
  healthy: '正常',
  unavailable: '不可用',
  unknown: '未知',
  disabled: '已停用',
}

const REASON_LABEL: Record<string, string> = {
  probe_succeeded: '探测通过',
  sampler_running: '采样进行中',
  feature_disabled: '功能未启用',
  observation_expired: '观测过期',
  not_configured: '尚未配置',
  // A component nobody has probed yet is not a fault; saying "未观测" is more
  // accurate than borrowing "unknown", which reads like a measurement failed.
  not_observed: '尚未观测',
}

function componentLabel(name: string): string {
  return COMPONENT_LABEL[name] ?? name
}
function stateLabel(state: string): string {
  return STATE_LABEL[state] ?? state
}
function reasonLabel(reason: string): string {
  if (reason === '') return ''
  return REASON_LABEL[reason] ?? reason
}

export function Operations() {
  const admin = useAuthStore((s) => s.user?.role === 'admin')
  const [job, setJob] = useState('')
  const [auditFamily, setAuditFamily] = useState('')
  const [hideCleanup, setHideCleanup] = useState(false)
  const components = useAPI<Schema<'Component'>[]>(
    '/api/v1/operations/components',
    admin,
    5000
  )
  const status = useAPI<Schema<'Job'>>(
    '/api/v1/jobs/' + job,
    admin && !!job,
    job ? 1000 : false
  )
  const site = useAPI<Schema<'Site'>>('/api/v1/site', admin)
  if (!admin) return <Forbidden />
  const displayTime = (value: string) =>
    new Date(value).toLocaleString('zh-CN', {
      timeZone: site.data?.timezone || 'Asia/Shanghai',
      hour12: false,
    })
  const unhealthy = (components.data ?? []).filter(
    (c) => c.enabled && c.state !== 'healthy'
  )
  return (
    // One flex column with one gap replaces per-card margins: the cards used to
    // carry their own mb-6 while others did not, so spacing drifted.
    <div className='flex flex-col gap-5'>
      <PageTitle
        title='运维与审计'
        description='组件状态每 5 秒刷新；过期探测显示为未知，而不是当作正常。'
      />
      <nav aria-label='页内导航' className='-mt-2 flex flex-wrap gap-1.5'>
        {ANCHORS.map((anchor) => (
          <a
            key={anchor.id}
            href={'#' + anchor.id}
            className='rounded-full border border-border px-3 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground'
          >
            {anchor.label}
          </a>
        ))}
      </nav>

      {/* First on the page: what is broken right now outranks how the components
          are doing, and the component table is the detail behind these rows. */}
      <AbnormalEvents zone={site.data?.timezone || ''} />

      <Panel title='组件状态' anchor='components' spaced={false}>
        <QueryState
          pending={components.isPending}
          error={components.error}
          retry={() => components.refetch()}
        />
        {unhealthy.length > 0 && (
          <p className='mb-3 text-sm text-amber-500'>
            {unhealthy.length} 个组件未处于正常状态：
            {unhealthy.map((c) => componentLabel(c.name)).join('、')}
          </p>
        )}
        <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4'>
          {components.data?.map((c) => (
            <article
              key={c.name}
              className='rounded-lg border p-3'
              data-testid={`component-${c.name}`}
            >
              <div className='flex items-center justify-between gap-2'>
                <h3 className='font-medium'>{componentLabel(c.name)}</h3>
                <StatusDot
                  tone={
                    !c.enabled
                      ? 'neutral'
                      : c.state === 'healthy'
                        ? 'success'
                        : c.state === 'unavailable'
                          ? 'destructive'
                          : 'warning'
                  }
                  label={c.enabled ? stateLabel(c.state) : '功能未启用'}
                />
              </div>
              <p className='mt-1 text-xs text-muted-foreground'>
                {reasonLabel(c.reason) || '—'} ·{' '}
                {c.observed_at ? displayTime(c.observed_at) : '尚未观测'}
              </p>
              {/* The probe code is the support lookup key, so it stays — but it
                  must not be the line the eye lands on in a healthy column. */}
              <p
                className='mt-1 truncate font-mono text-[10px] text-muted-foreground/40'
                title={`${c.name} · ${c.reason}`}
              >
                {c.name}
                {c.reason ? ' · ' + c.reason : ''}
              </p>
            </article>
          ))}
        </div>
      </Panel>

      <ReliabilityOperations />

      <Card id='audit' className='scroll-mt-24'>
        <CardHeader>
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <CardTitle className='text-base'>操作审计</CardTitle>
            <AuditFamilyFilter
              value={auditFamily}
              onChange={setAuditFamily}
              hideCleanup={hideCleanup}
              onHideCleanupChange={setHideCleanup}
            />
          </div>
        </CardHeader>
        <CardContent>
          <AuditTrail family={auditFamily} hideCleanup={hideCleanup} />
        </CardContent>
      </Card>

      <details className='rounded-lg border'>
        <summary className='cursor-pointer px-4 py-3 text-lg font-medium'>
          按任务 ID 查询
        </summary>
        <div className='px-4 pb-4'>
          <p className='mb-3 text-xs text-muted-foreground'>
            任务需要从接口或日志取得 ID 才能查询；这不是常用的运维动作，平时不需要展开。
          </p>
          <form
            className='flex flex-wrap items-end gap-3'
            onSubmit={(event) => {
              event.preventDefault()
              setJob(text(fields(event.currentTarget), 'job_id'))
            }}
          >
            <Field
              label='任务 ID'
              name='job_id'
              placeholder='例如 3f2a…'
              required
            />
            <Button>查询任务</Button>
          </form>
          {job && (
            <div className='mt-4'>
              <QueryState
                pending={status.isPending}
                error={status.error}
                retry={() => status.refetch()}
              />
              {status.data && (
                <dl className='grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm'>
                  <dt className='text-muted-foreground'>类型</dt>
                  <dd>{status.data.kind}</dd>
                  <dt className='text-muted-foreground'>状态</dt>
                  <dd>{status.data.state}</dd>
                  <dt className='text-muted-foreground'>尝试次数</dt>
                  <dd>{status.data.attempts}</dd>
                  <dt className='text-muted-foreground'>诊断</dt>
                  <dd>{status.data.error_code || '无'}</dd>
                </dl>
              )}
            </div>
          )}
        </div>
      </details>
    </div>
  )
}
