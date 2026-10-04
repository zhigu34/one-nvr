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
        description='登记 /storage 下已经挂载的目录。底层 NAS、RAID 或文件系统由部署环境管理。'
      />
      <Panel title='添加存储池'>
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
            按文件系统去重：{query.data.capacity.filesystem_count} 个 · 总容量{' '}
            {bytes(query.data.capacity.total_bytes)} · 可用{' '}
            {bytes(query.data.capacity.free_bytes)}
          </p>
          {query.data.items.length === 0 && <Empty>尚未添加存储池。</Empty>}
          {query.data.items.map((pool) => (
            <PoolRow key={`${pool.id}:${pool.version}`} pool={pool} />
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
        状态：{pool.state} · 业务占用：
        {pool.used_bytes === null ? '已验证录像占用待核查' : bytes(pool.used_bytes)}<span className='text-muted-foreground ms-1 text-xs'>（仅已索引录像；未发布占用未知）</span>
      </p>
      <div className='mb-4 grid gap-2 md:grid-cols-3'>
        {pool.checks.map((check) => (
          <div key={check.service} className='rounded-lg border p-3 text-sm'>
            <p>
              {check.service} · {check.state}
            </p>
            <p className='mt-1 text-xs text-muted-foreground'>
              {check.reason === 'test_source_required'
                ? '待 ZLM 测试视频源'
                : check.reason}
            </p>
          </div>
        ))}
      </div>
      <Notices {...action} />
      <form
        className='flex flex-wrap items-end gap-4'
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
        <label className='flex items-center gap-2 pb-2'>
          <input type='checkbox' name='enabled' defaultChecked={pool.enabled} />
          启用
        </label>
        <label className='flex items-center gap-2 pb-2'>
          <input
            type='checkbox'
            name='is_default'
            defaultChecked={pool.is_default}
          />
          默认池
        </label>
        <Button disabled={action.pending}>保存设置</Button>
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
        <Button
          type='button'
          variant='destructive'
          disabled={action.pending}
          onClick={() => {
            if (
              window.confirm('移除这个空存储池的登记？目录与池身份文件将保留。')
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
      {job && (
        <div className='mt-3 text-xs'>
          <QueryState
            pending={status.isPending}
            error={status.error}
            retry={() => status.refetch()}
          />
          <p>
            任务 {job} · {status.data?.state || '等待查询'}
          </p>
          {status.data?.error_code && <p>{status.data.error_code}</p>}
        </div>
      )}
    </Panel>
  )
}
