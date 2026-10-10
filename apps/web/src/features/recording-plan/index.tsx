import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest } from '@/lib/api-client'
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
import {
  failureText,
  verifyPoolWrite,
  type PoolCheckOutcome,
} from './pool-check'

const PLANNED_MODES = [
  { label: '定时录像（后续开放）', value: 'scheduled' },
  { label: '事件录像（后续开放）', value: 'event' },
]

export function RecordingPlan() {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const client = useQueryClient()
  const batch = useAction()
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
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [mode, setMode] = useState('continuous')
  const [outcome, setOutcome] = useState('')

  const items = channels.data?.items || []
  const permitted = items.filter((channel) =>
    channel.permissions.includes('configure')
  )
  const chosen = permitted.filter((channel) => selected.has(channel.id))

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
          mode: mode as 'none' | 'continuous',
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
    if (stale.length === 0 || mode !== 'continuous')
      return { items, recovered: 0, reasons }
    const poolByChannel = new Map<string, string>()
    for (const row of stale) {
      try {
        const status = await apiRequest<Schema<'ChannelSourceStatus'>>(
          `/api/v1/channels/${row.channel_id}/source/status`
        )
        if (status.storage_pool_id)
          poolByChannel.set(row.channel_id, status.storage_pool_id)
      } catch {
        // The row keeps its original rejection reason.
      }
    }
    // A channel without its own binding falls back to the default pool,
    // exactly like the server does when it applies the mode.
    if (stale.some((row) => !poolByChannel.has(row.channel_id))) {
      try {
        const pools = await apiRequest<PageData<Schema<'Pool'>>>(
          '/api/v1/storage-pools?limit=100'
        )
        const fallback = pools.items.find(
          (pool) => pool.is_default && pool.enabled
        )
        if (fallback) {
          for (const row of stale)
            if (!poolByChannel.has(row.channel_id))
              poolByChannel.set(row.channel_id, fallback.id)
        }
      } catch {
        // Rows without a usable pool keep their original reason.
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

  return (
    <>
      <PageTitle
        title='录像计划'
        description='按通道设置录像方式。手动即连续录像；定时与事件后续开放。'
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
          <div className='mb-4 flex flex-wrap items-end gap-3 rounded-xl border bg-card p-4'>
            <SelectField
              label='批量录像方式'
              className='text-sm'
              value={mode}
              onValueChange={setMode}
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
            <Button
              type='button'
              disabled={batch.pending || chosen.length === 0}
              onClick={() => void applyBatch()}
            >
              应用到选中通道
            </Button>
            <span className='pb-2 text-xs text-muted-foreground'>
              已选 {chosen.length} / {permitted.length} 路可配置通道
            </span>
            {chosen.length > 0 && (
              <Button
                type='button'
                variant='ghost'
                onClick={() => setSelected(new Set())}
              >
                取消选择
              </Button>
            )}
          </div>
          <div className='overflow-hidden rounded-xl border bg-card'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className='w-10'>
                    <Checkbox
                      aria-label='选择全部可配置通道'
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
                  <TableHead>录像方式</TableHead>
                  <TableHead>存储池</TableHead>
                  <TableHead>操作</TableHead>
                  <TableHead>结果</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((channel) => (
                  <PolicyRow
                    key={channel.id}
                    channel={channel}
                    admin={admin}
                    pools={pools.data?.items || []}
                    selected={selected.has(channel.id)}
                    onSelect={(checked) => toggle(channel.id, checked)}
                    onApplied={() => void client.invalidateQueries()}
                  />
                ))}
              </TableBody>
            </Table>
          </div>
        </>
      )}
    </>
  )
}
