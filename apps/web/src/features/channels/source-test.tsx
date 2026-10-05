import { useEffect, useRef, useState } from 'react'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Notices } from '@/features/foundation/ui'
import { sourceCommand } from './api'
import { useNow } from './use-now'

type Props = {
  channelId: string
  version: number
  revisionId: string
  proof: Schema<'SourceTestResult'> | null
  first: boolean
  hasPool: boolean
  onAccepted: (value: Schema<'SourceChange'>, kind: 'test' | 'apply') => void
}
export function SourceTestControls({
  channelId,
  version,
  revisionId,
  proof,
  first,
  hasPool,
  onAccepted,
}: Props) {
  const now = useNow()
  const [mode, setMode] = useState<'none' | 'continuous'>('continuous')
  const [pending, setPending] = useState(false),
    [notice, setNotice] = useState(''),
    [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])
  const valid =
    proof?.revision_id === revisionId &&
    proof.state === 'succeeded' &&
    proof.main.state === 'healthy' &&
    !!proof.expires_at &&
    Date.parse(proof.expires_at) > now
  async function run(kind: 'test' | 'apply') {
    if (pending) return
    const abort = new AbortController()
    controller.current = abort
    setPending(true)
    setError('')
    setNotice('')
    const body: Schema<'SourceApplyInput'> = {
      revision_id: revisionId,
      test_id: proof?.id || '',
    }
    if (first) body.first_recording_mode = mode
    try {
      const value = await sourceCommand<Schema<'SourceChange'>>(
        `/api/v1/channels/${channelId}/${kind === 'test' ? `source-revisions/${revisionId}/test` : 'source/apply'}`,
        kind === 'test' ? {} : body,
        kind === 'apply' ? version : undefined,
        abort.signal
      )
      if (!abort.signal.aborted) {
        setNotice(kind === 'test' ? '测试已排队' : '应用已排队，等待执行结果')
        onAccepted(value, kind)
      }
    } catch (e) {
      if (!abort.signal.aborted)
        setError(e instanceof Error ? e.message : '请求失败')
    } finally {
      if (!abort.signal.aborted) setPending(false)
    }
  }
  return (
    <section className='grid gap-3'>
      <Notices error={error} notice={notice} />
      {first && (
        <label className='grid gap-2 text-sm'>
          首次普通录像
          <select
            aria-label='首次普通录像'
            value={mode}
            onChange={(e) => setMode(e.target.value as typeof mode)}
            className='rounded-md border bg-background p-2'
          >
            <option value='continuous'>开启连续录像</option>
            <option value='none'>关闭录像，仅取流</option>
          </select>
        </label>
      )}
      {first && mode === 'continuous' && !hasPool && (
        <p className='text-sm text-amber-600'>请先绑定存储池，或关闭普通录像</p>
      )}
      {proof && (
        <div className='rounded-lg border p-3 text-sm'>
          <p>
            测试状态：
            {
              {
                queued: '排队中',
                testing: '测试中',
                succeeded: '通过',
                failed: '失败',
                cancelled: '已取消',
              }[proof.state]
            }
          </p>
          <p>
            主流：{proof.main.state} · 子流：{proof.sub.state}
          </p>
          {valid && proof.sub.state === 'unavailable' && (
            <p>子流不可用，可降级应用主流</p>
          )}
          <p>
            测试有效至：
            {proof.expires_at
              ? new Date(proof.expires_at).toLocaleString()
              : '尚无可用结果'}
          </p>
        </div>
      )}
      <p className='text-xs text-muted-foreground'>
        测试不会切换当前取流。通过后再应用；连续录像所需码率证据过期时，请重新测试。
      </p>
      <div className='flex gap-3'>
        <Button
          type='button'
          variant='outline'
          disabled={pending}
          onClick={() => void run('test')}
        >
          测试取流
        </Button>
        <Button
          type='button'
          disabled={
            pending || !valid || (first && mode === 'continuous' && !hasPool)
          }
          onClick={() => void run('apply')}
        >
          应用配置
        </Button>
      </div>
    </section>
  )
}
