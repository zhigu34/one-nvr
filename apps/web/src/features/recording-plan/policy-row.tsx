import { useEffect, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError, apiRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { TableCell, TableRow } from '@/components/ui/table'
import { Notices, SelectField } from '@/features/foundation/ui'
import { StatusBadge } from '@/features/foundation/status-badge'
import { useAPI } from '@/features/foundation/hooks'
import { sourceCommand } from '@/features/channels/api'
import { settleJob, verifyPoolWrite } from '@/features/storage-pools/pool-check'
import { cn } from '@/lib/utils'

/**
 * Only the modes the control plane implements are selectable. The rest are
 * listed so the operator can see the direction of travel without being offered
 * capability that does not exist yet.
 */
const PLANNED_MODES = [
  { label: '定时录像（后续开放）', value: 'scheduled' },
  { label: '事件录像（后续开放）', value: 'event' },
]

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
  const [succeeded, setSucceeded] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])

  const label = channelLabel(channel)
  const configured = channel.permissions.includes('configure')
  // No applied camera means nothing to record and no pool to bind: the whole
  // row goes inert until the channel is connected (same rule as detection).
  const unconfigured =
    !status.data?.current_revision_id &&
    status.data?.main?.state === 'not_configured'
  const inert = unconfigured || !configured
  const mode = draftMode ?? policy.data?.mode ?? 'none'
  const boundPool = status.data?.storage_pool_id || ''
  const pool = draftPool ?? boundPool
  const poolName =
    pools.find((entry) => entry.id === boundPool)?.name || '未绑定'
  // Row health, derived from the same status payload the channels page uses.
  const health = !status.data
    ? 'unknown'
    : unconfigured
      ? 'unconfigured'
      : !channel.enabled
        ? 'disabled'
        : status.data.recording?.state === 'healthy'
          ? 'recording'
          : status.data.main?.state === 'healthy'
            ? 'online'
            : 'offline'
  const HEALTH: Record<
    typeof health,
    { tone: 'success' | 'neutral' | 'warning'; text: string } | null
  > = {
    recording: { tone: 'success', text: '录制中' },
    online: { tone: 'success', text: '在线' },
    offline: { tone: 'warning', text: '离线' },
    disabled: { tone: 'neutral', text: '通道已停用' },
    unconfigured: { tone: 'neutral', text: '未配置源' },
    unknown: null,
  }
  const badge = HEALTH[health]

  async function sendWithRecovery(
    kind: 'policy' | 'pool',
    body: unknown,
    version: number,
    abort: AbortController
  ) {
    const endpoint = `/api/v1/channels/${channel.id}/${kind === 'policy' ? 'recording-policy' : 'storage-pool'}`
    const send = () =>
      sourceCommand<Schema<'SourceChange'>>(
        endpoint,
        body,
        version,
        abort.signal,
        'PUT'
      )
    try {
      return await send()
    } catch (e) {
      if (abort.signal.aborted) return null
      // The write proof is only valid for 30 seconds. Instead of asking the
      // operator to act faster, recover by running one real pool check and
      // retrying once; the evidence requirement itself is unchanged.
      const target = kind === 'pool' ? draftPool : boundPool || null
      if (
        !(e instanceof ApiError) ||
        e.code !== 'zlm_write_evidence_unavailable' ||
        !target
      )
        throw e
      setNotice('存储池写入检查已过期，正在自动重新验证…')
      const verified = await verifyPoolWrite(target, abort.signal)
      if (abort.signal.aborted) return null
      if (!verified.ok) {
        setError(verified.message)
        return null
      }
      setNotice('验证通过，正在继续…')
      return await send()
    }
  }

  async function run(kind: 'policy' | 'pool', body: unknown, version: number) {
    if (pending) return
    const abort = new AbortController()
    controller.current = abort
    setPending(kind)
    setError('')
    setNotice('')
    setSucceeded('')
    try {
      // Smart chaining: applying a mode on a row whose pool dropdown was also
      // changed binds the pool first, so enabling recording cannot fail on a
      // forgotten bind. The version still comes from the live status read.
      if (kind === 'policy' && draftPool !== null) {
        const fresh = await apiRequest<Schema<'ChannelSourceStatus'>>(
          `/api/v1/channels/${channel.id}/source/status`
        )
        if (abort.signal.aborted) return
        await sendWithRecovery(
          'pool',
          { pool_id: draftPool },
          fresh.version,
          abort
        )
        if (abort.signal.aborted) return
        setDraftPool(null)
      }
      const change = await sendWithRecovery(kind, body, version, abort)
      if (abort.signal.aborted || change === null) return
      if (kind === 'policy') setDraftMode(null)
      else setDraftPool(null)
      setNotice('已排队，等待执行结果')
      onApplied()
      await client.invalidateQueries()
      const settled = await settleJob(change.job_id, abort.signal)
      if (abort.signal.aborted) return
      if (settled.state === 'succeeded') {
        setNotice('')
        setSucceeded(kind)
      } else if (settled.state === 'failed') setError(settled.message)
    } catch (e) {
      if (!abort.signal.aborted) setError(message(e))
    } finally {
      if (!abort.signal.aborted) setPending('')
    }
  }

  const policyReady = !policy.isPending && !policy.error && !!policy.data
  const poolDirty = admin && draftPool !== null
  const modeDirty = draftMode !== null
  return (
    <TableRow
      data-testid='policy-row'
      className={cn(
        health === 'recording' && 'bg-emerald-950/15 hover:bg-emerald-950/25',
        inert && 'opacity-40'
      )}
    >
      <TableCell>
        <Checkbox
          aria-label={`选择 ${label}`}
          className='rounded border-zinc-600'
          checked={selected}
          disabled={inert}
          onCheckedChange={(state) => onSelect(state === true)}
        />
      </TableCell>
      <TableCell className='font-mono text-xs'>{label}</TableCell>
      <TableCell>
        <div className='flex items-center gap-2'>
          <span className='font-medium'>{channel.channel_name}</span>
          {badge && (
            <span className='inline-flex shrink-0'>
              <StatusBadge tone={badge.tone}>{badge.text}</StatusBadge>
            </span>
          )}
        </div>
      </TableCell>
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
            disabled={inert || !!pending}
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
              disabled={inert || !!pending}
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
              size='sm'
              disabled={inert || !!pending || !poolDirty || !status.data}
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
              <Link
                to='/storage-pools'
                search={{}}
                aria-label={`${label} 检查存储池`}
                title='前往存储池页检查写入能力'
                className='inline-flex h-7 items-center gap-0.5 rounded-full border px-2 text-xs text-muted-foreground hover:bg-accent hover:text-foreground'
              >
                ↗ 存储池
              </Link>
            )}
          </div>
        ) : (
          <span className='text-sm text-muted-foreground'>{poolName}</span>
        )}
      </TableCell>
      <TableCell>
        <div className='flex items-center gap-2'>
          <Button
            type='button'
            size='sm'
            disabled={
              inert ||
              !policyReady ||
              !!pending ||
              !modeDirty ||
              (mode === 'continuous' && !boundPool && draftPool === null)
            }
            onClick={() => void run('policy', { mode }, policy.data?.version || 0)}
          >
            应用
          </Button>
          {succeeded && (
            <span
              data-testid={`applied-${succeeded}`}
              className='inline-flex items-center gap-1 rounded-full border border-emerald-500/20 bg-emerald-500/10 px-2 py-0.5 text-xs text-emerald-400'
            >
              ✓ 已生效
            </span>
          )}
        </div>
      </TableCell>
      <TableCell>
        <Notices error={error} notice={notice} />
        {mode === 'continuous' && !boundPool && draftPool === null && !inert && (
          <p className='text-xs text-amber-600'>请先绑定存储池</p>
        )}
        {inert && (
          <p className='text-xs text-muted-foreground'>请先在通道管理接入摄像头</p>
        )}
      </TableCell>
    </TableRow>
  )
}
