import { useState } from 'react'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema, PageData } from '@/lib/types'
import { HardDrive, Plus, Settings2, RotateCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { useAPI, useAction, fields, text } from '@/features/foundation/hooks'
import { LoadingButton } from '@/features/foundation/loading-button'
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
import { cn } from '@/lib/utils'

type Pools = PageData<Schema<'Pool'>> & { capacity: Schema<'StorageCapacity'> }
function bytes(value: number) {
  return (value / 1024 / 1024 / 1024).toFixed(1) + ' GiB'
}

const POOL_STATE_LABELS: Record<Schema<'Pool'>['state'], string> = {
  ready: '正常',
  pending: '待检查',
  unavailable: '不可用',
  low_space: '空间不足',
  disabled: '已停用',
}

const CHECK_STATE_BADGES: Record<
  Schema<'PoolCheck'>['state'],
  { label: string; cls: string; card: string }
> = {
  healthy: {
    label: '正常 (检查通过)',
    cls: 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20',
    card: '',
  },
  unavailable: {
    label: '不可用',
    cls: 'bg-red-500/10 text-red-400 border-red-500/20',
    card: 'border-red-500/30 bg-red-950/10',
  },
  pending: {
    label: '待检查',
    cls: 'bg-amber-500/10 text-amber-400 border-amber-500/20',
    card: 'border-amber-500/30 bg-amber-950/10',
  },
  expired: {
    label: '检查已过期',
    cls: 'bg-amber-500/10 text-amber-400 border-amber-500/20',
    card: 'border-amber-500/30 bg-amber-950/10',
  },
}

const CHECK_REASON_LABELS: Record<string, string> = {
  read_write_delete_verified: '读写删三项验证通过',
  observation_expired: '观测过期',
  test_source_required: '需先完成摄像头连接测试',
}

export function StoragePools() {
  const admin = useAuthStore((s) => s.user?.role === 'admin')
  const [cursor, setCursor] = useState('')
  const [showAdd, setShowAdd] = useState(false)
  const query = useAPI<Pools>(
    `/api/v1/storage-pools?limit=100${cursor ? '&cursor=' + cursor : ''}`,
    admin,
    5000
  )
  const action = useAction(true)
  if (!admin) return <Forbidden />
  return (
    <>
      <div className='mb-6 flex flex-wrap items-start justify-between gap-4'>
        <PageTitle
          title='存储池'
          description='添加已挂载的目录作为录像存储池，再在通道中选择使用。'
        />
        <Button
          type='button'
          variant={showAdd ? 'outline' : 'default'}
          aria-expanded={showAdd}
          onClick={() => setShowAdd((value) => !value)}
        >
          <Plus aria-hidden className='size-4' />
          {showAdd ? '收起添加表单' : '添加存储池'}
        </Button>
      </div>
      {showAdd && (
        <Panel title='添加存储池'>
          <p className='mb-4 text-sm text-muted-foreground'>
            填写容器内的挂载路径：部署时 ONE_NVR_STORAGE_ROOT 对应 /storage，例如
            /storage/disk1。无需配置底层磁盘。
          </p>
          <Notices {...action} />
          <form
            className='flex flex-wrap items-end gap-3'
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
            <div className='min-w-48 flex-1'>
              <Field label='存储池名称' name='name' maxLength={128} required />
            </div>
            <div className='min-w-48 flex-1'>
              <Field
                label='挂载目录'
                name='path'
                placeholder='/storage/disk1'
                required
              />
            </div>
            <LoadingButton
              className='h-9 shrink-0 px-5'
              pending={action.pending}
              pendingText='添加中…'
              type='submit'
            >
              确认添加
            </LoadingButton>
          </form>
        </Panel>
      )}
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

/** The segmented capacity bar: business bytes, other FS usage, then free. */
function CapacityBar({ pool }: { pool: Schema<'Pool'> }) {
  const check = pool.checks.find(
    (item) => item.service === 'api' || item.service === 'worker'
  )
  const total = check?.total_bytes ?? 0
  const free = check?.free_bytes ?? 0
  const used = pool.used_bytes
  if (!total) return null
  // The filesystem's own usage minus our indexed recording bytes: everything
  // the operator did not put there but the disk still holds. A sub-byte
  // float remainder must not paint a zero-width segment.
  const other =
    used === null ? null : Math.max(total - free - used, 0) > 1
      ? total - free - used
      : 0
  const usedPct =
    used === null ? null : Math.min(Math.round((used / total) * 100), 100)
  return (
    <div>
      <div className='mb-1.5 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 text-xs'>
        <span>
          业务录像占用{' '}
          <span className='font-medium tabular-nums text-foreground'>
            {used === null ? '待统计' : bytes(used)}
          </span>
          <span className='ms-1 text-muted-foreground'>
            （仅已索引录像；未发布占用未知）
          </span>
        </span>
        <span className='tabular-nums text-muted-foreground'>
          可用 {bytes(free)} / 共 {bytes(total)}
          {usedPct !== null ? ` (已用 ${usedPct}%)` : ''}
        </span>
      </div>
      <div
        data-testid='capacity-bar'
        className='flex h-2.5 w-full overflow-hidden rounded-full bg-zinc-800/80'
      >
        {used !== null && used > 0 && (
          <div
            data-testid='capacity-used'
            className='bg-emerald-500'
            style={{ width: `${(used / total) * 100}%` }}
          />
        )}
        {other !== null && other > 0 && (
          <div
            data-testid='capacity-other'
            className='bg-zinc-600'
            style={{ width: `${(other / total) * 100}%` }}
          />
        )}
      </div>
    </div>
  )
}

function PoolRow({ pool }: { pool: Schema<'Pool'> }) {
  const action = useAction(true)
  const [job, setJob] = useState('')
  const [confirmRemove, setConfirmRemove] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [checksOpen, setChecksOpen] = useState(false)
  const status = useAPI<Schema<'Job'>>(
    `/api/v1/jobs/${job}`,
    !!job,
    job ? 1000 : false
  )
  const poolTone =
    pool.state === 'ready'
      ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
      : pool.state === 'unavailable' || pool.state === 'low_space'
        ? 'bg-red-500/10 text-red-400 border-red-500/20'
        : 'bg-amber-500/10 text-amber-400 border-amber-500/20'
  return (
    <Panel title=''>
      <div className='flex flex-wrap items-center justify-between gap-4 pb-3'>
        <div className='flex min-w-0 flex-wrap items-center gap-2.5'>
          <HardDrive aria-hidden className='size-5 shrink-0 text-emerald-400' />
          <span className='text-lg font-semibold'>{pool.name}</span>
          <code className='truncate rounded bg-zinc-900 px-2 py-0.5 font-mono text-xs text-zinc-400'>
            {pool.path}
          </code>
          {pool.is_default && (
            <span className='rounded-full border border-emerald-500/20 bg-emerald-500/10 px-2 py-0.5 text-xs text-emerald-400'>
              默认池
            </span>
          )}
          <span
            className={cn(
              pool.enabled ? '' : 'border-zinc-500/20 bg-zinc-500/10 text-zinc-400',
              'rounded-full border px-2 py-0.5 text-xs',
              pool.enabled && poolTone
            )}
          >
            {pool.enabled ? '已启用' : '已停用'}
          </span>
        </div>
        <div className='flex shrink-0 items-center gap-2'>
          <span
            className={cn(
              'inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs',
              poolTone
            )}
          >
            <span
              aria-hidden
              className={cn(
                'size-1.5 rounded-full bg-current',
                pool.state === 'ready' && 'animate-pulse'
              )}
            />
            {POOL_STATE_LABELS[pool.state]}
          </span>
          <Button
            type='button'
            variant='outline'
            size='sm'
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
            <RotateCw aria-hidden className='size-3.5' />
            立即检查
          </Button>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            aria-expanded={settingsOpen}
            onClick={() => setSettingsOpen((value) => !value)}
          >
            <Settings2 aria-hidden className='size-3.5' />
            存储池设置
          </Button>
        </div>
      </div>
      <Notices {...action} />
      <div className='space-y-3 pt-3'>
        <CapacityBar pool={pool} />
        <details
          open={checksOpen}
          onToggle={(event) =>
            setChecksOpen((event.target as HTMLDetailsElement).open)
          }
          className='text-sm'
        >
          <summary className='cursor-pointer text-muted-foreground'>
            检查详情
          </summary>
          <div className='mt-3 grid gap-2 md:grid-cols-3'>
            {pool.checks.map((check) => {
              const badge = CHECK_STATE_BADGES[check.state]
              return (
                <div
                  key={check.service}
                  data-testid={`pool-check-${check.service}`}
                  className={cn(
                    'flex flex-col rounded-lg border p-3 text-sm',
                    badge.card
                  )}
                >
                  <div className='flex items-center justify-between gap-2'>
                    <p className='font-medium'>
                      {
                        { api: '目录访问', worker: '后台写入', zlm: '录像写入' }[
                          check.service
                        ]
                      }
                    </p>
                    <span
                      className={cn(
                        'inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs',
                        badge.cls
                      )}
                    >
                      <span
                        aria-hidden
                        className={cn(
                          'size-1.5 rounded-full bg-current',
                          check.state === 'healthy' && 'animate-pulse'
                        )}
                      />
                      {badge.label}
                    </span>
                  </div>
                  <p className='mt-1 text-xs text-muted-foreground'>
                    {check.reason === 'test_source_required'
                      ? '需先完成摄像头连接测试'
                      : check.state === 'healthy'
                        ? '读写删三项验证通过'
                        : '请重新检查或查看诊断'}
                  </p>
                  <p
                    className='mt-auto pt-2 font-mono text-[11px] text-zinc-500'
                    title={CHECK_REASON_LABELS[check.reason] || check.reason}
                  >
                    {check.service} · {check.reason || 'no_probe_yet'}
                  </p>
                </div>
              )
            })}
          </div>
        </details>
        {pool.checks.some(
          (check) => check.reason === 'test_source_required'
        ) && (
          <p className='text-sm text-amber-600'>等待摄像头连接测试</p>
        )}
        {settingsOpen && (
          <form
            key={pool.version}
            className='flex flex-wrap items-end gap-4 rounded-lg border p-3'
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
              variant='outline'
              className='ml-auto border-red-500/30 bg-red-500/10 text-red-400 hover:bg-red-500/20 hover:text-red-300'
              disabled={action.pending}
              onClick={() => setConfirmRemove(true)}
            >
              移除登记
            </Button>
          </form>
        )}
      </div>
      <ConfirmDialog
        open={confirmRemove}
        onOpenChange={setConfirmRemove}
        title='移除这个空存储池的登记？'
        desc='目录与池身份文件将保留；已绑定该池的通道会先拒绝解绑。'
        confirmText='移除登记'
        cancelBtnText='取消'
        destructive
        isLoading={action.pending}
        handleConfirm={() =>
          void action.run(
            () =>
              apiRequest(
                `/api/v1/storage-pools/${pool.id}`,
                jsonRequest('DELETE', undefined, pool.version)
              ),
            '登记已移除'
          )
        }
      />
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
