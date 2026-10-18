import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Table,
  TableBody,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  PageTitle,
  QueryState,
  Notices,
  Empty,
  SelectField,
} from '@/features/foundation/ui'
import { useAPI, useAction } from '@/features/foundation/hooks'
import { sourceCommand } from '@/features/channels/api'
import { PolicyRow } from './policy-row'
import { MANUAL_DURATIONS } from './durations'
import {
  boundOrDefaultPool,
  failureText,
  verifyPoolWrite,
  type PoolCheckOutcome,
} from '@/features/storage-pools/pool-check'

const PLACEHOLDER_MODES = [{ label: '事件录像（后续开放）', value: 'event' }]



export function RecordingPlan() {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const client = useQueryClient()
  const batch = useAction()
  const manualBatch = useAction()
  const channels = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100',
    true,
    10000
  )
  const pools = useAPI<PageData<Schema<'Pool'>>>(
    '/api/v1/storage-pools?limit=100',
    admin,
    10000
  )
  const summaries = useAPI<{ items: Schema<'ChannelSummary'>[] }>(
    '/api/v1/channels/summary'
  )
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [mode, setMode] = useState('continuous')
  const [poolID, setPoolID] = useState('')
  const [outcome, setOutcome] = useState('')
  const [manualMinutes, setManualMinutes] = useState(30)
  const [dirtyRows, setDirtyRows] = useState<Set<string>>(new Set())

  const items = channels.data?.items || []
  const summaryOf = new Map(
    (summaries.data?.items || []).map((item) => [item.channel_id, item])
  )
  // Same rule as the channels and detection pages: no applied camera source
  // means the row cannot record, so it stays out of every batch surface.
  const unconfigured = (channel: Schema<'Channel'>) => {
    const summary = summaryOf.get(channel.id ?? '')
    return (
      !summary?.source_ip &&
      !summary?.current_revision_id &&
      summary?.main.state === 'not_configured'
    )
  }
  const configured = items.filter((channel) => !unconfigured(channel))
  const permitted = configured.filter((channel) =>
    channel.permissions.includes('configure')
  )
  const chosen = permitted.filter((channel) => selected.has(channel.id))

  // Stable per-channel callbacks keep the row's dirty-report effect from
  // re-running on every parent render.
  const onDirty = (id: string) => (dirty: boolean) =>
    setDirtyRows((current) => {
      const next = new Set(current)
      if (dirty) next.add(id)
      else next.delete(id)
      return next
    })

  function toggle(id: string, checked: boolean) {
    setSelected((previous) => {
      const next = new Set(previous)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  // The batch carries one expected version per row, so read the current version
  // immediately before writing instead of trusting a page that may be stale.
  async function submitPolicies(channelIDs: string[]) {
    const rows = await Promise.all(
      channelIDs.map(async (channelID) => {
        const policy = await apiRequest<Schema<'RecordingPolicy'>>(
          `/api/v1/channels/${channelID}/recording-policy`
        )
        return {
          channel_id: channelID,
          expected_version: policy.version,
          mode: mode as 'none' | 'continuous' | 'planned',
        }
      })
    )
    return sourceCommand<Schema<'RecordingPolicyBatch'>>(
      '/api/v1/recording-policies',
      { items: rows },
      undefined,
      undefined,
      'PUT'
    )
  }

  // Rows rejected for a stale write proof recover by running one real pool
  // check for each pool they depend on and retrying just those rows. The
  // server evidence gate stays untouched; only the required steps are taken
  // automatically instead of being left to the operator.
  async function recoverRejected(items: Schema<'RecordingPolicyOutcome'>[]) {
    const stale = items.filter(
      (row) =>
        row.state !== 'queued' &&
        row.error_code === 'zlm_write_evidence_unavailable'
    )
    const reasons = new Map<string, string>()
    // A planned batch records inside its windows, so it needs the same write
    // evidence recovery as continuous; only 'none' has nothing to prove.
    if (stale.length === 0 || mode === 'none')
      return { items, recovered: 0, reasons }
    const poolByChannel = new Map<string, string>()
    for (const row of stale) {
      try {
        // The channel's own binding, or the default pool the server falls
        // back to when applying the mode.
        const poolID = await boundOrDefaultPool(row.channel_id)
        if (poolID) poolByChannel.set(row.channel_id, poolID)
      } catch {
        // The row keeps its original rejection reason.
      }
    }
    const verified = new Map<string, PoolCheckOutcome>()
    for (const poolID of new Set(poolByChannel.values())) {
      try {
        verified.set(poolID, await verifyPoolWrite(poolID))
      } catch {
        verified.set(poolID, { ok: false, message: '' })
      }
    }
    const retryable = stale.filter((row) => {
      const poolID = poolByChannel.get(row.channel_id)
      return poolID !== undefined && verified.get(poolID)?.ok === true
    })
    for (const row of stale) {
      if (retryable.includes(row)) continue
      const poolID = poolByChannel.get(row.channel_id)
      if (!poolID) {
        reasons.set(row.channel_id, '未绑定存储池，请先在「存储池」列绑定')
        continue
      }
      const result = verified.get(poolID)
      reasons.set(
        row.channel_id,
        result && !result.ok && result.message
          ? result.message
          : '存储池写入检查未通过或已过期'
      )
    }
    if (retryable.length === 0) return { items, recovered: 0, reasons }
    const retried = await submitPolicies(retryable.map((row) => row.channel_id))
    const merged = items.map(
      (row) =>
        retried.items.find((next) => next.channel_id === row.channel_id) || row
    )
    return {
      items: merged,
      recovered: retried.items.filter((row) => row.state === 'queued').length,
      reasons,
    }
  }

  async function applyBatch() {
    if (chosen.length === 0) return
    setOutcome('')
    await batch.run(
      async () => {
        // A picked pool binds first, sequentially, so enabling recording
        // cannot fail on a forgotten bind; the mode batch then walks the rows.
        if (admin && poolID) {
          for (const channel of chosen) {
            const status = await apiRequest<Schema<'ChannelSourceStatus'>>(
              `/api/v1/channels/${channel.id}/source/status`
            )
            if (status.storage_pool_id === poolID) continue
            await sourceCommand<Schema<'SourceChange'>>(
              `/api/v1/channels/${channel.id}/storage-pool`,
              { pool_id: poolID },
              status.version,
              undefined,
              'PUT'
            )
          }
          await client.invalidateQueries()
        }
        const first = await submitPolicies(chosen.map((channel) => channel.id))
        const { items, recovered, reasons } = await recoverRejected(first.items)
        const queued = items.filter((row) => row.state === 'queued')
        const rejected = items.filter((row) => row.state !== 'queued')
        const note =
          recovered > 0 ? `（其中 ${recovered} 路经自动重新验证存储池写入）` : ''
        setOutcome(
          rejected.length === 0
            ? `${queued.length} 路已排队${note}`
            : `${queued.length} 路已排队${note}，${rejected.length} 路未应用：${[
                ...new Set(
                  rejected.map(
                    (row) =>
                      reasons.get(row.channel_id) ||
                      failureText(row.error_code || '')
                  )
                ),
              ].join('、')}`
        )
        if (rejected.length === 0) setSelected(new Set())
      },
      '批量应用已提交',
      true
    )
  }

  // One channel at a time: parallel binds race each other's version checks
  // and pile into the same worker queue, which is how a batch of 8 turned
  // into a pile of conflicts. Rows already on the target pool are skipped and
  // reported as such; every row gets its own result, never a blanket verdict.
  async function bindBatch() {
    if (chosen.length === 0 || !poolID) return
    const poolName =
      (pools.data?.items || []).find((entry) => entry.id === poolID)?.name ||
      '所选存储池'
    setOutcome('')
    await batch.run(
      async () => {
        const skipped: string[] = []
        const ok: string[] = []
        const failed: string[] = []
        for (const channel of chosen) {
          const label = `CH${String(channel.channel_no).padStart(2, '0')}`
          try {
            const status = await apiRequest<Schema<'ChannelSourceStatus'>>(
              `/api/v1/channels/${channel.id}/source/status`
            )
            if (status.storage_pool_id === poolID) {
              skipped.push(label)
              continue
            }
            await sourceCommand<Schema<'SourceChange'>>(
              `/api/v1/channels/${channel.id}/storage-pool`,
              { pool_id: poolID },
              status.version,
              undefined,
              'PUT'
            )
            ok.push(label)
          } catch (e) {
            failed.push(
              `${label}: ${
                e instanceof Error && e.message ? e.message : '操作失败'
              }`
            )
          }
        }
        const parts: string[] = []
        if (ok.length) parts.push(`${ok.length} 路已绑定 ${poolName}`)
        if (skipped.length) parts.push(`${skipped.length} 路已在该池，跳过`)
        if (failed.length) parts.push(`失败：${failed.join('；')}`)
        setOutcome(parts.join(' · ') || '没有需要绑定的通道')
        if (failed.length === 0) setSelected(new Set())
      }, '批量绑定已执行')
  }

  // Manual recording is a request, not a mode, so it cannot ride the batch
  // mode select: "start" and "stop" are different verbs and a select can only
  // express one value. It gets its own duration control and its own buttons.
  //
  // Sequential like the bind batch, for the same reason: each start opens a
  // window with its own audit row, and a burst of parallel writes into the
  // same worker queue is how a batch of eight becomes a pile of conflicts.
  // Every row gets its own result — a partial batch must not read as success.
  async function batchManual(action: 'start' | 'stop') {
    if (chosen.length === 0) return
    setOutcome('')
    await manualBatch.run(
      async () => {
        const ok: string[] = []
        const failed: string[] = []
        for (const channel of chosen) {
          const label = `CH${String(channel.channel_no).padStart(2, '0')}`
          try {
            if (action === 'start') {
              await apiRequest(
                `/api/v1/channels/${channel.id}/manual-recording`,
                jsonRequest('POST', { minutes: manualMinutes })
              )
            } else {
              await apiRequest(
                `/api/v1/channels/${channel.id}/manual-recording`,
                { method: 'DELETE' }
              )
            }
            ok.push(label)
          } catch (e) {
            failed.push(
              `${label}: ${
                e instanceof Error && e.message ? e.message : '操作失败'
              }`
            )
          }
        }
        const verb = action === 'start' ? '已开始手动录像' : '已停止手动录像'
        const parts: string[] = []
        if (ok.length) parts.push(`${ok.length} 路${verb}`)
        if (failed.length) parts.push(`失败：${failed.join('；')}`)
        setOutcome(parts.join(' · '))
        if (failed.length === 0) setSelected(new Set())
        await client.invalidateQueries()
      },
      action === 'start' ? '批量手动录像已提交' : '批量停止已提交',
      false
    )
  }

  return (
    <>
      <PageTitle
        title='录像计划'
        description='按通道设置录像方式。连续录像全天录制；定时与事件后续开放。'
      />
      <QueryState
        pending={channels.isPending}
        error={channels.error}
        retry={() => channels.refetch()}
      />
      <Notices error={batch.error} notice={batch.notice} />
      {outcome && (
        <p role='status' className='mb-4 text-sm'>
          {outcome}
        </p>
      )}
      {channels.data && items.length === 0 && (
        <Empty>没有已授权的通道。请联系管理员分配权限。</Empty>
      )}
      {items.length > 0 && (
        <>
          <div className='mb-4 flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-card px-4 py-2.5 shadow-2xs'>
            <div className='flex flex-wrap items-center gap-x-4 gap-y-2'>
              <span className='text-sm font-medium whitespace-nowrap'>
                已选 {chosen.length} / {permitted.length} 路可配置通道
                {dirtyRows.size > 0 && (
                  <span className='ms-1 text-xs font-normal text-amber-600'>
                    · {dirtyRows.size} 路有未保存修改
                  </span>
                )}
              </span>
              {admin && (
                <>
                  <div className='flex items-center gap-2'>
                    <span className='text-xs text-muted-foreground'>
                      批量存储池:
                    </span>
                    <SelectField
                      ariaLabel='批量存储池'
                      className='w-36'
                      triggerClassName='min-w-0 [&>span[data-slot=select-value]]:truncate'
                      value={poolID}
                      onValueChange={setPoolID}
                      options={[
                        { value: '', label: '选择存储池' },
                        ...(pools.data?.items || []).map((entry) => ({
                          value: entry.id,
                          label: `${entry.name}${entry.enabled ? '' : ' · 已停用'}`,
                          disabled: !entry.enabled,
                        })),
                      ]}
                    />
                    <Button
                      type='button'
                      size='sm'
                      disabled={batch.pending || chosen.length === 0 || !poolID}
                      onClick={() => void bindBatch()}
                    >
                      批量绑定
                    </Button>
                  </div>
                </>
              )}
              <div className='flex items-center gap-2'>
                <span className='text-xs text-muted-foreground'>
                  批量录像方式:
                </span>
                <SelectField
                  ariaLabel='批量录像方式'
                  className='w-40'
                  triggerClassName='min-w-0 [&>span[data-slot=select-value]]:truncate'
                  value={mode}
                  onValueChange={setMode}
                  options={[
                    { value: 'none', label: '关闭录像' },
                    { value: 'continuous', label: '连续录像' },
                    { value: 'planned', label: '定时录像' },
                    ...PLACEHOLDER_MODES.map((entry) => ({
                      value: entry.value,
                      label: entry.label,
                      disabled: true,
                    })),
                  ]}
                />
                <Button
                  type='button'
                  size='sm'
                  disabled={batch.pending || chosen.length === 0}
                  onClick={() => void applyBatch()}
                >
                  批量应用
                </Button>
              </div>
              {/* Manual recording is a request rather than a stored mode, so
                  it cannot ride the batch mode select above: start and stop
                  are different verbs and one select cannot express both. It is
                  labelled 限时录像 so the two controls beside each other say
                  different things — the mode select sets a policy, this one
                  opens a window that expires on its own (REC-03/04). */}
              <div className='flex items-center gap-2 border-s ps-4'>
                <span className='text-xs text-muted-foreground'>
                  批量限时录像:
                </span>
                <SelectField
                  ariaLabel='批量限时录像时长'
                  className='w-fit'
                  triggerClassName='min-w-0 [&>span[data-slot=select-value]]:truncate'
                  value={String(manualMinutes)}
                  onValueChange={(value) => setManualMinutes(Number(value))}
                  options={MANUAL_DURATIONS.map((minutes) => ({
                    value: String(minutes),
                    label: `${minutes} 分钟`,
                  }))}
                />
                <Button
                  type='button'
                  size='sm'
                  disabled={manualBatch.pending || chosen.length === 0}
                  onClick={() => void batchManual('start')}
                >
                  开始录像
                </Button>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  disabled={manualBatch.pending || chosen.length === 0}
                  onClick={() => void batchManual('stop')}
                >
                  停止
                </Button>
              </div>
              {chosen.length > 0 && (
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  onClick={() => setSelected(new Set())}
                >
                  取消选择
                </Button>
              )}
            </div>
          </div>
          <div className='overflow-hidden rounded-xl border bg-card'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className='w-10'>
                    <Checkbox
                      aria-label='选择全部可配置通道'
                      className='rounded border-zinc-600'
                      checked={
                        permitted.length > 0 &&
                        permitted.every((channel) => selected.has(channel.id))
                      }
                      disabled={permitted.length === 0}
                      onCheckedChange={(state) =>
                        setSelected(
                          state === true
                            ? new Set(permitted.map((channel) => channel.id))
                            : new Set()
                        )
                      }
                    />
                  </TableHead>
                  <TableHead>通道</TableHead>
                  <TableHead>名称</TableHead>
                  <TableHead>存储池</TableHead>
                  <TableHead>录像方式</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((channel) => (
                  <PolicyRow
                    key={channel.id}
                    channel={channel}
                    summary={summaryOf.get(channel.id ?? '')}
                    admin={admin}
                    pools={pools.data?.items || []}
                    schedulePeers={permitted
                      .filter((peer) => peer.id !== channel.id)
                      .map((peer) => ({
                        id: peer.id,
                        label: `CH${String(peer.channel_no).padStart(2, '0')}`,
                      }))}
                    selected={selected.has(channel.id)}
                    onSelect={(checked) => toggle(channel.id, checked)}
                    onApplied={() => void client.invalidateQueries()}
                    onDirtyChange={onDirty(channel.id ?? '')}
                  />
                ))}
              </TableBody>
            </Table>
            {items.length === 0 && (
              <div className='p-8 text-center text-sm text-muted-foreground'>
                没有匹配的通道
              </div>
            )}
          </div>
        </>
      )}
    </>
  )
}
