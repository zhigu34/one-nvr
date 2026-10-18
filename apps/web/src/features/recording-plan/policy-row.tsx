import { useEffect, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError, apiRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { TableCell, TableRow } from '@/components/ui/table'
import { SelectField } from '@/features/foundation/ui'
import { StatusBadge } from '@/features/foundation/status-badge'
import { sourceCommand } from '@/features/channels/api'
import { settleJob, verifyPoolWrite } from '@/features/storage-pools/pool-check'
import { cn } from '@/lib/utils'
import { ManualRecordingControl } from './manual-recording'

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
  summary,
  admin,
  pools,
  selected,
  onSelect,
  onApplied,
  onDirtyChange,
}: {
  channel: Schema<'Channel'>
  summary?: Schema<'ChannelSummary'>
  admin: boolean
  pools: Schema<'Pool'>[]
  selected: boolean
  onSelect: (checked: boolean) => void
  onApplied: () => void
  onDirtyChange: (dirty: boolean) => void
}) {
  const client = useQueryClient()
  // The recording plan reads mode, pool and health from the one summary
  // response the page already fetched: a status request per row was the N+1
  // that slowed first paint on a 32-channel site. Writes still read the live
  // version right before they send. `mode`/`pool` overlay the operator's
  // unsaved draft on the stored value, exactly like the channels page.
  const storedMode = summary?.recording_mode ?? 'none'
  const boundPool = summary?.storage_pool_id || ''
  const [draftMode, setDraftMode] = useState<string | null>(null)
  const [draftPool, setDraftPool] = useState<string | null>(null)
  const mode = draftMode ?? storedMode
  const [pending, setPending] = useState('')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [succeeded, setSucceeded] = useState('')
  const [errorFrom, setErrorFrom] = useState<'policy' | 'pool' | ''>('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])

  const label = channelLabel(channel)
  const configured = channel.permissions.includes('configure')
  // No applied camera means nothing to record and no pool to bind: the whole
  // row goes inert until the channel is connected (same rule as detection).
  const unconfigured =
    !summary?.current_revision_id && summary?.main.state === 'not_configured'
  const inert = unconfigured || !configured
  const pool = draftPool ?? boundPool
  const poolName =
    pools.find((entry) => entry.id === boundPool)?.name || '未绑定'
  const health = !summary
    ? 'unknown'
    : unconfigured
      ? 'unconfigured'
      : !channel.enabled
        ? 'disabled'
        : summary.recording.state === 'healthy'
          ? 'recording'
          : summary.main.state === 'healthy'
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
  const poolDirty = admin && draftPool !== null && draftPool !== boundPool
  // Compare the draft with the stored value, not with the overlaid display
  // value: `mode` already folds the draft in, so comparing against it would
  // make the edit state collapse the moment the dropdown commits.
  const modeDirty = draftMode !== null && draftMode !== storedMode
  // Report only on an actual transition: an unstable parent callback in the
  // effect deps would otherwise loop render → report → setState → render.
  const dirty = poolDirty || modeDirty
  const reported = useRef(false)
  useEffect(() => {
    if (dirty !== reported.current) {
      reported.current = dirty
      onDirtyChange(dirty)
    }
  }, [dirty, onDirtyChange])

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
      const target = kind === 'pool' ? draftPool : boundPool || draftPool
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
        setErrorFrom(kind)
        return null
      }
      setNotice('验证通过，正在继续…')
      return await send()
    }
  }

  async function run(kind: 'policy' | 'pool') {
    if (pending || inert) return
    const abort = new AbortController()
    controller.current = abort
    setPending(kind)
    setError('')
    setErrorFrom(kind)
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
        const change = await sendWithRecovery(
          'pool',
          { pool_id: draftPool },
          fresh.version,
          abort
        )
        if (abort.signal.aborted || change === null) return
        setDraftPool(null)
        await client.invalidateQueries()
      }
      const version =
        kind === 'policy'
          ? // The policy write expects the policy's own version; the status
            // read a moment ago carries the freshest one on this page.
            (
              await apiRequest<Schema<'RecordingPolicy'>>(
                `/api/v1/channels/${channel.id}/recording-policy`
              )
            ).version
          : (
              await apiRequest<Schema<'ChannelSourceStatus'>>(
                `/api/v1/channels/${channel.id}/source/status`
              )
            ).version
      if (abort.signal.aborted) return
      const body = kind === 'policy' ? { mode: draftMode ?? mode } : { pool_id: draftPool }
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
      } else if (settled.state === 'failed') {
        setError(settled.message || '任务失败')
        setErrorFrom(kind)
      }
    } catch (e) {
      if (!abort.signal.aborted) {
        setError(message(e))
        setErrorFrom(kind)
      }
    } finally {
      if (!abort.signal.aborted) setPending('')
    }
  }

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
        {inert ? (
          // Static placeholder: unconfigured rows render no interactive
          // controls at all, so 16 selects never mount for empty slots.
          <div className='flex items-center gap-2'>
            <span className='text-zinc-500'>{channel.channel_name}</span>
            <span className='inline-flex shrink-0'>
              <StatusBadge tone='neutral'>未配置源</StatusBadge>
            </span>
          </div>
        ) : (
          <div className='flex items-center gap-2'>
            <span className='font-medium'>{channel.channel_name}</span>
            {badge && (
              <span className='inline-flex shrink-0'>
                <StatusBadge tone={badge.tone}>{badge.text}</StatusBadge>
              </span>
            )}
          </div>
        )}
      </TableCell>
      <TableCell>
        {admin ? (
          inert ? (
            <span className='text-xs text-zinc-600'>—</span>
          ) : (
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
                size='sm'
                className={cn(poolDirty && 'bg-emerald-500 text-zinc-950 font-medium hover:bg-emerald-400')}
                disabled={!!pending || !poolDirty}
                onClick={() => void run('pool')}
              >
                绑定
              </Button>
              {succeeded === 'pool' && (
                <span
                  data-testid='applied-pool'
                  className='inline-flex items-center gap-1 rounded-full border border-emerald-500/20 bg-emerald-500/10 px-2 py-0.5 text-xs text-emerald-400'
                >
                  ✓ 已绑定
                </span>
              )}
              {boundPool && (
                <Link
                  to='/storage'
                  search={{ tab: 'pools' }}
                  aria-label={`${label} 检查存储池`}
                  title='前往存储池页检查写入能力'
                  className='inline-flex h-7 items-center gap-0.5 rounded-full border px-2 text-xs text-muted-foreground hover:bg-accent hover:text-foreground'
                >
                  ↗ 存储池
                </Link>
              )}
            </div>
          )
        ) : (
          <span className='text-sm text-muted-foreground'>{poolName}</span>
        )}
        {errorFrom === 'pool' && error && succeeded !== 'pool' && (
          <p role='alert' className='mt-1 text-xs text-red-500'>
            ✕ {error}
          </p>
        )}
        {errorFrom === 'pool' && !error && notice && (
          <p className='mt-1 text-xs text-muted-foreground'>{notice}</p>
        )}
      </TableCell>
      <TableCell>
        {inert ? (
          <span className='text-xs text-zinc-600'>—</span>
        ) : (
          <div className='flex flex-wrap items-center gap-2'>
            <SelectField
              ariaLabel={`${label} 录像方式`}
              className='text-sm'
              value={mode}
              disabled={!!pending}
              onValueChange={setDraftMode}
              options={[
                { value: 'none', label: '关闭录像' },
                { value: 'continuous', label: '连续录像' },
                ...PLANNED_MODES.map((entry) => ({
                  value: entry.value,
                  label: entry.label,
                  disabled: true,
                })),
              ]}
            />
            <Button
              type='button'
              size='sm'
              className={cn(modeDirty && 'bg-emerald-500 text-zinc-950 font-medium hover:bg-emerald-400')}
              disabled={!!pending || !modeDirty || mode === 'continuous' && !boundPool && draftPool === null}
              onClick={() => void run('policy')}
            >
              应用
            </Button>
            {succeeded === 'policy' && (
              <span
                data-testid='applied-policy'
                className='inline-flex items-center gap-1 rounded-full border border-emerald-500/20 bg-emerald-500/10 px-2 py-0.5 text-xs text-emerald-400'
              >
                ✓ 已生效
              </span>
            )}
            {errorFrom === 'policy' && error && (
              <span className='text-xs text-red-500' role='alert'>
                ✕ {error}
              </span>
            )}
            {errorFrom === 'policy' && !error && notice && succeeded !== 'policy' && (
              <span className='text-xs text-muted-foreground'>{notice}</span>
            )}
            {/* Manual recording is a separate request, not a mode: it runs
                alongside whatever the policy says and expires on its own
                (PRD REC-03/04). */}
            <ManualRecordingControl
              channelId={channel.id}
              label={label}
              disabled={!!pending}
            />
            {mode === 'continuous' && !boundPool && draftPool === null && !inert && (
              <p className='text-xs text-amber-600'>请先绑定存储池</p>
            )}
          </div>
        )}
      </TableCell>
    </TableRow>
  )
}
