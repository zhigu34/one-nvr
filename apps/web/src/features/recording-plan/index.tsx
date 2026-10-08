import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { PageTitle, QueryState, Notices, Empty } from '@/features/foundation/ui'
import { useAPI, useAction } from '@/features/foundation/hooks'
import { sourceCommand } from '@/features/channels/api'
import { PolicyRow } from './policy-row'

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
  async function applyBatch() {
    if (chosen.length === 0) return
    setOutcome('')
    await batch.run(
      async () => {
        const rows = await Promise.all(
          chosen.map(async (channel) => {
            const policy = await apiRequest<Schema<'RecordingPolicy'>>(
              `/api/v1/channels/${channel.id}/recording-policy`
            )
            return {
              channel_id: channel.id,
              expected_version: policy.version,
              mode: mode as 'none' | 'continuous',
            }
          })
        )
        const result = await sourceCommand<Schema<'RecordingPolicyBatch'>>(
          '/api/v1/recording-policies',
          { items: rows },
          undefined,
          undefined,
          'PUT'
        )
        const queued = result.items.filter((row) => row.state === 'queued')
        const rejected = result.items.filter((row) => row.state !== 'queued')
        setOutcome(
          rejected.length === 0
            ? `${queued.length} 路已排队`
            : `${queued.length} 路已排队，${rejected.length} 路未应用：${[
                ...new Set(rejected.map((row) => row.error_code || '未知原因')),
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
            <label className='grid gap-2 text-sm'>
              批量录像方式
              <select
                aria-label='批量录像方式'
                className='rounded-md border bg-background p-2'
                value={mode}
                onChange={(event) => setMode(event.target.value)}
              >
                <option value='none'>关闭录像</option>
                <option value='continuous'>手动（连续录像）</option>
                {PLANNED_MODES.map((entry) => (
                  <option key={entry.value} value={entry.value} disabled>
                    {entry.label}
                  </option>
                ))}
              </select>
            </label>
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
                    <input
                      type='checkbox'
                      aria-label='选择全部可配置通道'
                      checked={
                        permitted.length > 0 &&
                        permitted.every((channel) => selected.has(channel.id))
                      }
                      disabled={permitted.length === 0}
                      onChange={(event) =>
                        setSelected(
                          event.target.checked
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
