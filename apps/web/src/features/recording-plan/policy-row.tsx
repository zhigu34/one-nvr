import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { TableCell, TableRow } from '@/components/ui/table'
import { Notices, SelectField } from '@/features/foundation/ui'
import { useAPI } from '@/features/foundation/hooks'
import { sourceCommand } from '@/features/channels/api'

/**
 * Only the modes the control plane implements are selectable. The rest are
 * listed so the operator can see the direction of travel without being offered
 * capability that does not exist yet.
 */
const PLANNED_MODES = [
  { label: '定时录像（后续开放）', value: 'scheduled' },
  { label: '事件录像（后续开放）', value: 'event' },
]

function pause(signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    signal.throwIfAborted()
    const cancel = () => {
      clearTimeout(timer)
      reject(signal.reason)
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', cancel)
      resolve()
    }, 1000)
    signal.addEventListener('abort', cancel, { once: true })
  })
}

function channelLabel(channel: Schema<'Channel'>) {
  return `CH${String(channel.channel_no).padStart(2, '0')}`
}

function message(error: unknown) {
  return error instanceof Error && error.message
    ? error.message
    : '操作失败，请重试'
}

export function PolicyRow({
  channel,
  admin,
  pools,
  selected,
  onSelect,
  onApplied,
}: {
  channel: Schema<'Channel'>
  admin: boolean
  pools: Schema<'Pool'>[]
  selected: boolean
  onSelect: (checked: boolean) => void
  onApplied: () => void
}) {
  const client = useQueryClient()
  // The policy changes only through actions on this page, so it is read once
  // and refreshed after each write instead of polled. A stale version can only
  // produce an honest conflict, never a silent overwrite.
  const policy = useAPI<Schema<'RecordingPolicy'>>(
    `/api/v1/channels/${channel.id}/recording-policy`
  )
  const status = useAPI<Schema<'ChannelSourceStatus'>>(
    `/api/v1/channels/${channel.id}/source/status`,
    true,
    10000
  )
  // Drafts clear after a successful write so the controls follow the server
  // again instead of holding a stale edit.
  const [draftMode, setDraftMode] = useState<string | null>(null)
  const [draftPool, setDraftPool] = useState<string | null>(null)
  const [pending, setPending] = useState('')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])

  const label = channelLabel(channel)
  const configured = channel.permissions.includes('configure')
  const mode = draftMode ?? policy.data?.mode ?? 'none'
  const boundPool = status.data?.storage_pool_id || ''
  const pool = draftPool ?? boundPool
  const poolName =
    pools.find((entry) => entry.id === boundPool)?.name || '未绑定'

  async function run(kind: 'policy' | 'pool', body: unknown, version: number) {
    if (pending) return
    const abort = new AbortController()
    controller.current = abort
    setPending(kind)
    setError('')
    setNotice('')
    try {
      await sourceCommand<Schema<'SourceChange'>>(
        `/api/v1/channels/${channel.id}/${kind === 'policy' ? 'recording-policy' : 'storage-pool'}`,
        body,
        version,
        abort.signal,
        'PUT'
      )
      if (abort.signal.aborted) return
      if (kind === 'policy') setDraftMode(null)
      else setDraftPool(null)
      setNotice('已排队，等待执行结果')
      onApplied()
      await client.invalidateQueries()
    } catch (e) {
      if (!abort.signal.aborted) setError(message(e))
    } finally {
      if (!abort.signal.aborted) setPending('')
    }
  }

  // Pool evidence is a precondition for continuous recording, so the control
  // that produces it lives here rather than in the camera-connect flow.
  async function checkPool() {
    if (pending || !boundPool) return
    const abort = new AbortController()
    controller.current = abort
    setPending('check')
    setError('')
    setNotice('')
    try {
      const { job_id } = await apiRequest<{ job_id: string }>(
        `/api/v1/storage-pools/${boundPool}/test`,
        { ...jsonRequest('POST'), signal: abort.signal }
      )
      const deadline = Date.now() + 120000
      while (Date.now() < deadline) {
        abort.signal.throwIfAborted()
        const job = await apiRequest<Schema<'Job'>>(`/api/v1/jobs/${job_id}`, {
          signal: abort.signal,
        })
        if (job.state === 'succeeded') {
          setNotice('存储池检查已通过，现在可以启用录像')
          await client.invalidateQueries()
          return
        }
        if (job.state === 'failed') {
          setError('存储池检查未通过，请检查目录权限与剩余空间')
          return
        }
        await pause(abort.signal)
      }
      setError('存储池检查等待超时，请稍后在存储池页查看结果')
    } catch (e) {
      if (!abort.signal.aborted) setError(message(e))
    } finally {
      if (!abort.signal.aborted) setPending('')
    }
  }

  const policyReady = !policy.isPending && !policy.error && !!policy.data
  return (
    <TableRow data-testid='policy-row'>
      <TableCell>
        <Checkbox
          aria-label={`选择 ${label}`}
          checked={selected}
          disabled={!configured}
          onCheckedChange={(state) => onSelect(state === true)}
        />
      </TableCell>
      <TableCell className='font-mono text-xs'>{label}</TableCell>
      <TableCell className='font-medium'>{channel.channel_name}</TableCell>
      <TableCell>
        {policy.error ? (
          <span className='text-xs text-muted-foreground'>读取失败</span>
        ) : policy.isPending ? (
          <span className='text-xs text-muted-foreground'>读取中</span>
        ) : (
          <SelectField
            ariaLabel={`${label} 录像方式`}
            className='text-sm'
            value={mode}
            disabled={!configured || !!pending}
            onValueChange={setDraftMode}
            options={[
              { value: 'none', label: '关闭录像' },
              { value: 'continuous', label: '手动（连续录像）' },
              ...PLANNED_MODES.map((entry) => ({
                value: entry.value,
                label: entry.label,
                disabled: true,
              })),
            ]}
          />
        )}
      </TableCell>
      <TableCell>
        {admin ? (
          <div className='flex flex-wrap items-center gap-2'>
            <SelectField
              ariaLabel={`${label} 存储池`}
              className='text-sm'
              value={pool}
              disabled={!!pending}
              onValueChange={setDraftPool}
              options={[
                { value: '', label: '未绑定' },
                ...pools.map((entry) => ({
                  value: entry.id,
                  label: `${entry.name}${entry.enabled ? '' : ' · 已停用'}`,
                  disabled: !entry.enabled,
                })),
              ]}
            />
            <Button
              type='button'
              variant='outline'
              disabled={!!pending || draftPool === null || !status.data}
              onClick={() =>
                void run(
                  'pool',
                  { pool_id: draftPool },
                  status.data?.version || 0
                )
              }
            >
              绑定
            </Button>
            {boundPool && (
              <Button
                type='button'
                variant='ghost'
                disabled={!!pending}
                onClick={() => void checkPool()}
              >
                {pending === 'check' ? '检查中…' : '检查存储池'}
              </Button>
            )}
          </div>
        ) : (
          <span className='text-sm text-muted-foreground'>{poolName}</span>
        )}
      </TableCell>
      <TableCell>
        <Button
          type='button'
          disabled={
            !configured ||
            !policyReady ||
            !!pending ||
            draftMode === null ||
            (mode === 'continuous' && !boundPool)
          }
          onClick={() => void run('policy', { mode }, policy.data?.version || 0)}
        >
          应用
        </Button>
      </TableCell>
      <TableCell>
        <Notices error={error} notice={notice} />
        {draftMode === 'continuous' && !boundPool && (
          <p className='text-xs text-amber-600'>请先绑定存储池</p>
        )}
        {!configured && <p className='text-xs text-muted-foreground'>只读</p>}
      </TableCell>
    </TableRow>
  )
}
