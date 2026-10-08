import { useEffect, useRef, useState } from 'react'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Notices } from '@/features/foundation/ui'
import { sourceCommand } from './api'
import { StatusLabel } from './channel-status'
import { useNow } from './use-now'

type Props = {
  channelId: string
  version: number
  revisionId: string
  proof: Schema<'SourceTestResult'> | null
  first: boolean
  busy?: boolean
  onAccepted: (value: Schema<'SourceChange'>, kind: 'test' | 'apply') => void
}
// Bringing a camera up and deciding what to record are separate operations.
// This control only proves the stream and switches the source; it never asks
// for a recording mode, because a first apply starts stream-only and the
// recording plan page owns everything after that.
export function SourceTestControls({
  channelId,
  version,
  revisionId,
  proof,
  first,
  onAccepted,
  busy = false,
}: Props) {
  const now = useNow()
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
    if (pending || busy) return
    const abort = new AbortController()
    controller.current = abort
    setPending(true)
    setError('')
    setNotice('')
    const body: Schema<'SourceApplyInput'> = {
      revision_id: revisionId,
      test_id: proof?.id || '',
    }
    if (first) body.first_recording_mode = 'none'
    try {
      const value = await sourceCommand<Schema<'SourceChange'>>(
        `/api/v1/channels/${channelId}/${kind === 'test' ? `source-revisions/${revisionId}/test` : 'source/apply'}`,
        kind === 'test' ? {} : body,
        kind === 'apply' ? version : undefined,
        abort.signal
      )
      if (!abort.signal.aborted) {
        setNotice(kind === 'test' ? '测试已排队' : '启用已排队，等待执行结果')
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
          <div className='mt-2 flex flex-wrap gap-4'>
            <span className='flex items-center gap-2'>
              主流 <StatusLabel value={proof.main} />
            </span>
            <span className='flex items-center gap-2'>
              子流 <StatusLabel value={proof.sub} />
            </span>
          </div>
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
        测试不会切换当前取流。启用只接入取流；录像方式在「录像计划」页设置。
        测试证据过期后需要重新测试。
      </p>
      <div className='flex gap-3'>
        <Button
          type='button'
          variant='outline'
          disabled={pending || busy}
          onClick={() => void run('test')}
        >
          测试取流
        </Button>
        <Button
          type='button'
          disabled={pending || busy || !valid}
          onClick={() => void run('apply')}
        >
          测试并启用
        </Button>
      </div>
    </section>
  )
}
