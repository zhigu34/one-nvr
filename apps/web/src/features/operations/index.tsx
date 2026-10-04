import { useState } from 'react'
import { useAuthStore } from '@/stores/auth-store'
import type { Schema, PageData } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI, fields, text } from '@/features/foundation/hooks'
import {
  PageTitle,
  Panel,
  Field,
  QueryState,
  Forbidden,
  Empty,
} from '@/features/foundation/ui'

export function Operations() {
  const admin = useAuthStore((s) => s.user?.role === 'admin')
  const [job, setJob] = useState(''),
    [cursor, setCursor] = useState('')
  const components = useAPI<Schema<'Component'>[]>(
    '/api/v1/operations/components',
    admin,
    5000
  )
  const audits = useAPI<PageData<Schema<'Audit'>>>(
    `/api/v1/audit-logs?limit=50${cursor ? '&cursor=' + cursor : ''}`,
    admin
  )
  const status = useAPI<Schema<'Job'>>(
    `/api/v1/jobs/${job}`,
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
  return (
    <>
      <PageTitle
        title='运维与审计'
        description='组件每 5 秒刷新；过期探测显示未知。任务摘要与审计只包含公开诊断信息。'
      />
      <Panel title='组件状态'>
        <QueryState
          pending={components.isPending}
          error={components.error}
          retry={() => components.refetch()}
        />
        <div className='grid gap-3 md:grid-cols-2'>
          {components.data?.map((c) => (
            <article key={c.name} className='rounded-lg border p-4'>
              <h3 className='font-medium'>{c.name}</h3>
              <p className='my-2 text-sm'>
                {c.enabled ? c.state : '功能未启用'} · {c.reason}
              </p>
              <p className='text-xs text-muted-foreground'>
                最近观测{' '}
                {c.observed_at ? displayTime(c.observed_at) : '尚未观测'}
              </p>
            </article>
          ))}
        </div>
      </Panel>
      <Panel title='查询持久任务'>
        <form
          className='mb-4 flex items-end gap-3'
          onSubmit={(event) => {
            event.preventDefault()
            setJob(text(fields(event.currentTarget), 'job_id'))
          }}
        >
          <Field label='任务 ID' name='job_id' placeholder='UUID' required />
          <Button>查询任务</Button>
        </form>
        {job && (
          <>
            <QueryState
              pending={status.isPending}
              error={status.error}
              retry={() => status.refetch()}
            />
            {status.data && (
              <dl className='grid gap-2 text-sm'>
                <div>类型：{status.data.kind}</div>
                <div>状态：{status.data.state}</div>
                <div>尝试：{status.data.attempts}</div>
                <div>诊断：{status.data.error_code || '无'}</div>
              </dl>
            )}
          </>
        )}
      </Panel>
      <Panel title='操作审计'>
        <QueryState
          pending={audits.isPending}
          error={audits.error}
          retry={() => audits.refetch()}
        />
        {audits.data?.items.length === 0 && <Empty>尚无操作记录。</Empty>}
        <div className='overflow-x-auto'>
          <table className='w-full text-left text-sm'>
            <thead>
              <tr className='border-b'>
                <th className='py-3'>时间</th>
                <th>操作</th>
                <th>操作者</th>
                <th>对象</th>
              </tr>
            </thead>
            <tbody>
              {audits.data?.items.map((a) => (
                <tr key={a.id} className='border-b'>
                  <td className='py-3 pr-3 whitespace-nowrap'>
                    {displayTime(a.created_at)}
                  </td>
                  <td className='pr-3'>{a.action}</td>
                  <td className='max-w-40 truncate font-mono text-xs'>
                    {a.actor_id || '系统'}
                  </td>
                  <td className='max-w-40 truncate font-mono text-xs'>
                    {a.object_id || '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className='mt-4 flex gap-3'>
          {audits.data?.next_cursor && (
            <Button onClick={() => setCursor(audits.data!.next_cursor!)}>
              更早记录
            </Button>
          )}
          {cursor && (
            <Button variant='outline' onClick={() => setCursor('')}>
              返回最新
            </Button>
          )}
        </div>
      </Panel>
    </>
  )
}
