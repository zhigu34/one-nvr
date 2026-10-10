import { useEffect, useRef, useState } from 'react'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Notices } from '@/features/foundation/ui'
import { sourceCommand } from './api'
import { applyWithPoolRecovery, proveRevision } from './source-connect'
import { StatusLabel } from './channel-status'
import { useNow } from './use-now'

type Props = {
  channelId: string
  version: number
  revisionId: string
  revisionNumber?: number
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
  revisionNumber,
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
  // A proof is usable only when it belongs to this revision, actually proved a
  // first frame and has not expired. Anything else is re-taken by the click
  // that needs it, so the operator never has to fetch evidence by hand first —
  // and a proof for another revision can never authorize this one.
  const usable =
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
    try {
      if (kind === 'apply') {
        // Enabling needs proof that is valid right now. Missing or expired
        // evidence is taken inside this same click, so one action is always
        // enough and the 30-second window is never the operator's problem.
        let fresh = usable ? proof : null
        if (!fresh) {
          setNotice('正在测试摄像头连接…')
          fresh = await proveRevision(
            channelId,
            revisionId,
            abort.signal,
            (value) => onAccepted(value, 'test')
          )
        }
        const body: Schema<'SourceApplyInput'> = {
          revision_id: revisionId,
          test_id: fresh.id,
        }
        if (first) body.first_recording_mode = 'none'
        setNotice('正在启用…')
        // An apply while the channel records needs fresh pool write evidence;
        // the recovery hides that short-lived requirement behind this same
        // click instead of surfacing a 409 the operator would have to outrun.
        const value = await applyWithPoolRecovery(
          channelId,
          body,
          version,
          abort.signal,
          setNotice
        )
        if (!abort.signal.aborted) {
          setNotice('启用已排队，等待执行结果')
          onAccepted(value, 'apply')
        }
      } else {
        const value = await sourceCommand<Schema<'SourceChange'>>(
          `/api/v1/channels/${channelId}/source-revisions/${revisionId}/test`,
          {},
          undefined,
          abort.signal
        )
        if (!abort.signal.aborted) {
          setNotice('测试已排队')
          onAccepted(value, 'test')
        }
      }
    } catch (e) {
      if (!abort.signal.aborted)
        setError(e instanceof Error ? e.message : '请求失败')
    } finally {
      if (!abort.signal.aborted) setPending(false)
    }
  }
  return (
    <section className='grid content-start gap-3 rounded-lg border bg-card p-3'>
      <div className='flex items-center justify-between gap-2'>
        <span className='text-sm font-medium'>连接测试</span>
        {revisionNumber != null && (
          <span className='rounded border px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground'>
            修订 {revisionNumber}
          </span>
        )}
      </div>
      <Notices error={error} notice={notice} />
      {proof && (
        <div className='grid gap-2 rounded-md border bg-muted/25 p-2.5 text-xs'>
          <div className='flex items-center justify-between'>
            <span>主流</span>
            <StatusLabel value={proof.main} />
          </div>
          <div className='flex items-center justify-between'>
            <span>子流</span>
            <StatusLabel value={proof.sub} />
          </div>
          <p className='border-t pt-2 text-[11px] text-muted-foreground'>
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
            {proof.state === 'succeeded' &&
              (usable ? (
                <>
                  {' · 有效至 '}
                  {new Date(proof.expires_at!).toLocaleTimeString()}
                </>
              ) : (
                <span className='text-amber-700 dark:text-amber-400'>
                  {' '}
                  · 已过期或不属于当前修订，启用时会自动补测
                </span>
              ))}
          </p>
          {usable && proof.sub.state === 'unavailable' && (
            <p className='text-[11px] text-amber-700 dark:text-amber-400'>
              子流不可用，可降级应用主流
            </p>
          )}
        </div>
      )}
      <div className='grid gap-2'>
        <Button
          type='button'
          variant='outline'
          disabled={pending || busy}
          onClick={() => void run('test')}
        >
          测试取流
        </Button>
        {/* This applies the revision loaded here, which the form's own submit
            row does not do for a historical one. It is styled secondary because
            saving and enabling the form is the page's primary action. */}
        <Button
          type='button'
          variant='secondary'
          disabled={pending || busy}
          onClick={() => void run('apply')}
        >
          测试并启用
        </Button>
      </div>
      <p className='text-[11px] text-muted-foreground'>
        测试不切换当前取流。启用前需要有通过的测试，「测试并启用」会在证据缺失或过期时自动补测一次。
      </p>
    </section>
  )
}
