import { useMemo, useState } from 'react'
import { useAuthStore } from '@/stores/auth-store'
import type { PageData, Schema } from '@/lib/types'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Table,
  TableBody,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { PageTitle, QueryState, Empty } from '@/features/foundation/ui'
import { useAPI } from '@/features/foundation/hooks'
import { channelConfigured } from './mode-format'
import { PolicyRow } from './policy-row'
import { PlanConfigBar } from './plan-config-bar'

/**
 * The recording plan page: one configuration bar, one read-only table.
 *
 * The bar is the only place anything is changed. The table answers what each
 * channel ended up with. See PlanConfigBar for why the controls left the rows.
 */
export function RecordingPlan() {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
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
  const site = useAPI<Schema<'Site'>>('/api/v1/site')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  // Unconfigured rows collapse behind one line by default: sixteen mostly-empty
  // slots are setup context, not per-channel work, and they pushed the rows an
  // operator came for below the fold.
  const [showUnconfigured, setShowUnconfigured] = useState(false)

  const items = channels.data?.items || []
  const summaryOf = useMemo(
    () =>
      new Map(
        (summaries.data?.items || []).map((item) => [item.channel_id, item])
      ),
    [summaries.data]
  )
  const poolNames = useMemo(
    () =>
      new Map(
        (pools.data?.items || []).map((pool) => [pool.id ?? '', pool.name ?? ''])
      ),
    [pools.data]
  )
  const configured = items.filter((channel) =>
    channelConfigured(summaryOf.get(channel.id ?? ''))
  )
  const unconfigured = items.filter(
    (channel) => !channelConfigured(summaryOf.get(channel.id ?? ''))
  )
  // Only a channel the operator may configure can be selected: applying to a
  // channel the server will reject wastes a round trip per channel and reports a
  // failure the operator cannot act on.
  const permitted = configured.filter((channel) =>
    channel.permissions.includes('configure')
  )
  const chosen = permitted.filter((channel) => selected.has(channel.id))
  // A collapse that hides selected channels would silently drop them from the
  // next apply: expanding on selection keeps "what I see" equal to "what runs".
  const unconfiguredShown =
    showUnconfigured || unconfigured.some((channel) => selected.has(channel.id))
  const visibleRows = unconfiguredShown ? items : configured

  function toggle(id: string, checked: boolean) {
    setSelected((previous) => {
      const next = new Set(previous)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  const peers = permitted.map((channel) => ({
    id: channel.id ?? '',
    label: `CH${String(channel.channel_no).padStart(2, '0')}`,
  }))

  return (
    <div className='flex flex-col gap-4'>
      <PageTitle
        title='录像计划'
        description='在上方配置录像方式与事件录像，勾选通道后一次应用；下方表格只展示当前生效状态。'
      />
      <QueryState
        pending={channels.isPending}
        error={channels.error}
        retry={() => channels.refetch()}
      />
      {channels.data && items.length === 0 && (
        <Empty>没有已授权的通道。请联系管理员分配权限。</Empty>
      )}
      {items.length > 0 && (
        <>
          <PlanConfigBar
            chosen={chosen}
            chosenCount={chosen.length}
            total={permitted.length}
            onSelectAll={() =>
              setSelected(new Set(permitted.map((channel) => channel.id ?? '')))
            }
            onClear={() => setSelected(new Set())}
            summaries={summaryOf}
            peers={peers}
            timezone={site.data?.timezone || ''}
            pools={pools.data?.items || []}
          />
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
                            ? new Set(
                                permitted.map((channel) => channel.id ?? '')
                              )
                            : new Set()
                        )
                      }
                    />
                  </TableHead>
                  <TableHead className='w-20'>通道</TableHead>
                  <TableHead>名称</TableHead>
                  <TableHead>存储池</TableHead>
                  <TableHead>当前录像计划</TableHead>
                  <TableHead>更新时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visibleRows.map((channel) => (
                  <PolicyRow
                    key={channel.id}
                    channel={channel}
                    summary={summaryOf.get(channel.id ?? '')}
                    selected={selected.has(channel.id ?? '')}
                    onSelect={(checked) => toggle(channel.id ?? '', checked)}
                    zone={site.data?.timezone || ''}
                    poolNames={poolNames}
                  />
                ))}
              </TableBody>
            </Table>
            {visibleRows.length === 0 && (
              <div className='p-8 text-center text-sm text-muted-foreground'>
                没有匹配的通道
              </div>
            )}
          </div>
          {unconfigured.length > 0 && (
            <button
              type='button'
              className='w-full rounded-xl border border-dashed border-border px-4 py-2.5 text-sm text-muted-foreground hover:bg-accent/40'
              aria-expanded={unconfiguredShown}
              onClick={() => setShowUnconfigured((value) => !value)}
            >
              {unconfiguredShown
                ? '收起未配置源通道'
                : `已隐藏 ${unconfigured.length} 个未配置源通道（${unconfigured
                    .map(
                      (channel) =>
                        `CH${String(channel.channel_no).padStart(2, '0')}`
                    )
                    .join('、')}）· 点击展开`}
            </button>
          )}
        </>
      )}
    </div>
  )
}
