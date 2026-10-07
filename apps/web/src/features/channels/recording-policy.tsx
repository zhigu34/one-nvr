import { useEffect, useRef, useState } from 'react'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Notices } from '@/features/foundation/ui'
import { sourceCommand } from './api'

type Props = {
  channel: Schema<'Channel'>
  mode: Schema<'RecordingPolicy'>['mode']
  poolId: string | null
  pools: Schema<'Pool'>[]
  onAccepted: (value: Schema<'SourceChange'>) => void
}
export function RecordingPolicyControls({
  channel,
  mode,
  poolId,
  pools,
  onAccepted,
}: Props) {
  const [selected, setSelected] = useState(mode),
    [target, setTarget] = useState(poolId || '')
  const [pending, setPending] = useState(false),
    [notice, setNotice] = useState(''),
    [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])
  async function save(kind: 'policy' | 'pool') {
    if (pending) return
    const abort = new AbortController()
    controller.current = abort
    setPending(true)
    setError('')
    setNotice('')
    try {
      const value = await sourceCommand<Schema<'SourceChange'>>(
        `/api/v1/channels/${channel.id}/${kind === 'policy' ? 'recording-policy' : 'storage-pool'}`,
        kind === 'policy' ? { mode: selected } : { pool_id: target },
        channel.version,
        abort.signal,
        'PUT'
      )
      if (!abort.signal.aborted) {
        setNotice('操作已排队，等待执行结果')
        onAccepted(value)
      }
    } catch (e) {
      if (!abort.signal.aborted)
        setError(e instanceof Error ? e.message : '请求失败')
    } finally {
      if (!abort.signal.aborted) setPending(false)
    }
  }
  return (
    <section className='grid gap-4'>
      <Notices error={error} notice={notice} />
      <p className='text-sm text-muted-foreground'>
        先选择存储池，再设置录像方式。关闭录像后仍可取流，已有录像保留。
      </p>
      <div className='flex flex-wrap items-end gap-3 rounded-lg border p-4'>
        <label className='grid min-w-48 flex-1 gap-2 text-sm'>
          录像存储池
          <select
            aria-label='录像存储池'
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            className='rounded-md border bg-background p-2'
          >
            <option value=''>未绑定</option>
            {pools.map((pool) => (
              <option key={pool.id} value={pool.id} disabled={!pool.enabled}>
                {pool.name}
                {pool.enabled ? '' : ' · 已停用'}
              </option>
            ))}
          </select>
        </label>
        <Button
          type='button'
          variant='outline'
          disabled={pending || !target || target === poolId}
          onClick={() => void save('pool')}
        >
          绑定存储池
        </Button>
        <a href='/storage-pools' className='pb-2 text-sm underline'>
          管理存储池
        </a>
      </div>
      <div className='flex flex-wrap items-end gap-3 rounded-lg border p-4'>
        <label className='grid min-w-48 flex-1 gap-2 text-sm'>
          普通录像
          <select
            aria-label='普通录像'
            value={selected}
            onChange={(e) => setSelected(e.target.value as typeof selected)}
            className='rounded-md border bg-background p-2'
          >
            <option value='none'>关闭录像</option>
            <option value='continuous'>连续录像</option>
          </select>
        </label>
        <Button
          type='button'
          disabled={pending || (selected === 'continuous' && !poolId)}
          onClick={() => void save('policy')}
        >
          保存录像策略
        </Button>
        {selected === 'continuous' && !poolId && (
          <p className='w-full text-sm text-amber-600'>请先绑定存储池。</p>
        )}
      </div>
      <p className='text-xs text-muted-foreground'>
        切换存储池会检查新目录，历史录像仍保留在原位置。当前版本尚无自动清理，请关注剩余空间。
      </p>
    </section>
  )
}
