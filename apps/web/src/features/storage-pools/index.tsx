import { useState } from 'react'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema, PageData } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI, useAction, fields, text } from '@/features/foundation/hooks'
import {
  PageTitle,
  Panel,
  Field,
  CheckboxField,
  Notices,
  QueryState,
  Forbidden,
  Empty,
} from '@/features/foundation/ui'

type Pools = PageData<Schema<'Pool'>> & { capacity: Schema<'StorageCapacity'> }
function bytes(value: number) {
  return (value / 1024 / 1024 / 1024).toFixed(1) + ' GiB'
}
export function StoragePools() {
  const admin = useAuthStore((s) => s.user?.role === 'admin')
  const [cursor, setCursor] = useState('')
  const query = useAPI<Pools>(
    `/api/v1/storage-pools?limit=100${cursor ? '&cursor=' + cursor : ''}`,
    admin,
    5000
  )
  const action = useAction()
  if (!admin) return <Forbidden />
  return (
    <>
      <PageTitle
        title='存储池'
        description='添加已挂载的目录作为录像存储池，再在通道中选择使用。'
      />
      <Panel title='添加存储池'>
        <p className='mb-4 text-sm text-muted-foreground'>
          填写容器内的挂载路径：部署时 ONE_NVR_STORAGE_ROOT 对应 /storage，例如
          /storage/disk1。无需配置底层磁盘。
        </p>
        <Notices {...action} />
        <form
          className='grid gap-4 md:grid-cols-3'
          onSubmit={(event) => {
            event.preventDefault()
            const form = event.currentTarget
            const data = fields(form)
            void action.run(async () => {
              await apiRequest(
                '/api/v1/storage-pools',
                jsonRequest('POST', {
                  name: text(data, 'name'),
                  path: text(data, 'path'),
                })
              )
              form.reset()
            }, '存储池已登记')
          }}
        >
          <Field label='存储池名称' name='name' maxLength={128} required />
          <Field
            label='挂载目录'
            name='path'
            placeholder='/storage/disk1'
            required
          />
          <Button className='self-end' disabled={action.pending}>
            添加存储池
          </Button>
        </form>
      </Panel>
      <QueryState
        pending={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      {query.data && (
        <>
          <p className='mb-4 text-sm text-muted-foreground'>
            可用存储：{query.data.capacity.filesystem_count} 个文件系统 · 总容量{' '}
            {bytes(query.data.capacity.total_bytes)} · 可用{' '}
            {bytes(query.data.capacity.free_bytes)}
          </p>
          {query.data.items.length === 0 && <Empty>尚未添加存储池。</Empty>}
          {query.data.items.map((pool) => (
            <PoolRow key={pool.id} pool={pool} />
          ))}
          {query.data.next_cursor && (
            <Button onClick={() => setCursor(query.data!.next_cursor!)}>
              下一页
            </Button>
          )}
          {cursor && (
            <Button variant='outline' onClick={() => setCursor('')}>
              返回首页
            </Button>
          )}
        </>
      )}
    </>
  )
}
function PoolRow({ pool }: { pool: Schema<'Pool'> }) {
  const action = useAction()
  const [job, setJob] = useState('')
  const status = useAPI<Schema<'Job'>>(
    `/api/v1/jobs/${job}`,
    !!job,
    job ? 1000 : false
  )
  return (
    <Panel title={`${pool.name}${pool.is_default ? ' · 默认池' : ''}`}>
      <p className='mb-2 font-mono text-sm break-all'>{pool.path}</p>
      <p className='mb-3 text-sm text-muted-foreground'>
        状态：
        {
          {
            ready: '可用',
            pending: '待检查',
            unavailable: '不可用',
            low_space: '空间不足',
            disabled: '已停用',
          }[pool.state]
        }{' '}
        · 业务占用：
        {pool.used_bytes === null ? '待统计' : bytes(pool.used_bytes)}
        <span className='ms-1 text-xs text-muted-foreground'>
          （仅已索引录像；未发布占用未知）
        </span>
      </p>
      <Notices {...action} />
      <div className='mb-3 flex flex-wrap items-center gap-3'>
        <Button
          type='button'
          variant='outline'
          disabled={action.pending}
          onClick={() =>
            void action.run(async () => {
              const result = await apiRequest<{ job_id: string }>(
                `/api/v1/storage-pools/${pool.id}/test`,
                jsonRequest('POST')
              )
              setJob(result.job_id)
            }, '检查任务已排队')
          }
        >
          立即检查
        </Button>
      </div>
      <details className='rounded-lg border p-3'>
        <summary className='cursor-pointer text-sm font-medium'>
          存储池设置
        </summary>
        <form
          key={pool.version}
          className='mt-4 flex flex-wrap items-end gap-4'
          onSubmit={(event) => {
            event.preventDefault()
            const data = fields(event.currentTarget)
            void action.run(
              () =>
                apiRequest(
                  `/api/v1/storage-pools/${pool.id}`,
                  jsonRequest(
                    'PATCH',
                    {
                      name: text(data, 'name'),
                      enabled: data.has('enabled'),
                      is_default: data.has('is_default'),
                    },
                    pool.version
                  )
                ),
              '存储池设置已保存'
            )
          }}
        >
          <Field
            label='名称'
            id={`pool-${pool.id}`}
            name='name'
            defaultValue={pool.name}
            required
          />
          <CheckboxField
            label='启用'
            id={`pool-enabled-${pool.id}`}
            name='enabled'
            defaultChecked={pool.enabled}
            className='pb-2'
          />
          <CheckboxField
            label='默认池'
            id={`pool-default-${pool.id}`}
            name='is_default'
            defaultChecked={pool.is_default}
            className='pb-2'
          />
          <Button disabled={action.pending}>保存设置</Button>
          <Button
            type='button'
            variant='destructive'
            disabled={action.pending}
            onClick={() => {
              if (
                window.confirm(
                  '移除这个空存储池的登记？目录与池身份文件将保留。'
                )
              )
                void action.run(
                  () =>
                    apiRequest(
                      `/api/v1/storage-pools/${pool.id}`,
                      jsonRequest('DELETE', undefined, pool.version)
                    ),
                  '登记已移除'
                )
            }}
          >
            移除登记
          </Button>
        </form>
      </details>
      <details className='mt-3 text-sm'>
        <summary className='cursor-pointer text-muted-foreground'>
          检查详情
        </summary>
        <div className='mb-4 grid gap-2 md:grid-cols-3'>
          {pool.checks.map((check) => (
            <div key={check.service} className='rounded-lg border p-3 text-sm'>
              <p>
                {
                  { api: '目录访问', worker: '后台写入', zlm: '录像写入' }[
                    check.service
                  ]
                }{' '}
                ·{' '}
                {
                  {
                    healthy: '正常',
                    unavailable: '不可用',
                    pending: '待检查',
                    expired: '检查已过期',
                  }[check.state]
                }
              </p>
              <p className='mt-1 text-xs text-muted-foreground'>
                {check.reason === 'test_source_required'
                  ? '需先完成摄像头连接测试'
                  : check.state === 'healthy'
                    ? '检查通过'
                    : '请重新检查或查看诊断'}
              </p>
            </div>
          ))}
        </div>
        <div className='grid gap-2 text-xs text-muted-foreground'>
          {pool.checks.map((check) => (
            <p key={check.service}>
              {check.service} · {check.reason || '无附加诊断'}
            </p>
          ))}
        </div>
      </details>
      {pool.checks.some((check) => check.reason === 'test_source_required') && (
        <p className='mt-3 text-sm text-amber-600'>等待摄像头连接测试</p>
      )}
      {job && (
        <div className='mt-3 text-xs'>
          <QueryState
            pending={status.isPending}
            error={status.error}
            retry={() => status.refetch()}
          />
          <p>
            检查状态：
            {status.data
              ? {
                  queued: '排队中',
                  running: '检查中',
                  succeeded: '完成',
                  failed: '失败',
                }[status.data.state]
              : '等待查询'}
          </p>
          {status.data?.error_code && <p>{status.data.error_code}</p>}
        </div>
      )}
    </Panel>
  )
}
