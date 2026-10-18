import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { apiRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI, useAction } from '@/features/foundation/hooks'
import { sourceCommand } from '@/features/channels/api'
import { verifyPoolWrite } from './pool-check'
import { channelConfigured } from '@/features/recording-plan/mode-format'
import { cn } from '@/lib/utils'

/**
 * Per-pool channel binding panel: the recording plan shows which pool a channel
 * writes to; this panel is where that decision is made. It lists the channels a
 * pool serves and binds the pool to a picked set of channels in one pass, using
 * the same per-channel PUT the old row-level bind used — no new endpoint, no
 * new server state, only a different door to the same room.
 *
 * Sequential writes, like every batch on this product: parallel binds race each
 * other's version checks and pile into the same worker queue, which is how a
 * batch of eight became a pile of conflicts. Every channel reports its own
 * result; a partial batch must never read as success.
 */
export function PoolBinding({
  pool,
  enabled,
}: {
  pool: Schema<'Pool'>
  enabled: boolean
}) {
  const client = useQueryClient()
  const action = useAction(true)
  const [open, setOpen] = useState(false)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [result, setResult] = useState('')
  const summaries = useAPI<{ items: Schema<'ChannelSummary'>[] }>(
    '/api/v1/channels/summary',
    true,
    10_000
  )
  const channels = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100',
    true,
    10_000
  )
  const channelOf = useMemo(
    () => new Map((channels.data?.items || []).map((c) => [c.id ?? '', c])),
    [channels.data]
  )
  const configured = (summaries.data?.items || []).filter((item) =>
    channelConfigured(item)
  )
  const bound = configured.filter((item) => item.storage_pool_id === pool.id)
  const bindable = configured.filter(
    (item) => channelOf.get(item.channel_id)?.permissions.includes('configure')
  )
  const label = (item: Schema<'ChannelSummary'>) =>
    `CH${String(item.channel_no).padStart(2, '0')}`

  function toggle(id: string, checked: boolean) {
    setPicked((current) => {
      const next = new Set(current)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  function pickConfigured() {
    setPicked(new Set(bindable.map((item) => item.channel_id)))
  }

  async function bind() {
    if (picked.size === 0) return
    setResult('')
    await action.run(async () => {
      const ok: string[] = []
      const skipped: string[] = []
      const failed: string[] = []
      for (const [channelID] of picked) {
        const item = summaries.data?.items.find((entry) => entry.channel_id === channelID)
        const name = item ? label(item) : channelID
        try {
          const status = await apiRequest<Schema<'ChannelSourceStatus'>>(
            `/api/v1/channels/${channelID}/source/status`
          )
          if (status.storage_pool_id === pool.id) {
            skipped.push(name)
            continue
          }
          await sourceCommand<Schema<'SourceChange'>>(
            `/api/v1/channels/${channelID}/storage-pool`,
            { pool_id: pool.id },
            status.version,
            undefined,
            'PUT'
          )
          ok.push(name)
        } catch (cause) {
          // The 30-second write proof is the gate this page also recovers: one
          // real check, then a single retry, the same as the recording plan.
          if (
            cause instanceof Object &&
            'code' in cause &&
            (cause as { code?: string }).code ===
              'zlm_write_evidence_unavailable'
          ) {
            try {
              const verified = await verifyPoolWrite(pool.id)
              if (verified.ok) {
                const status = await apiRequest<Schema<'ChannelSourceStatus'>>(
                  `/api/v1/channels/${channelID}/source/status`
                )
                await sourceCommand<Schema<'SourceChange'>>(
                  `/api/v1/channels/${channelID}/storage-pool`,
                  { pool_id: pool.id },
                  status.version,
                  undefined,
                  'PUT'
                )
                ok.push(name)
                continue
              }
            } catch {
              // Falls through to the failure report below.
            }
          }
          failed.push(
            `${name}: ${cause instanceof Error && cause.message ? cause.message : '操作失败'}`
          )
        }
      }
      const parts: string[] = []
      if (ok.length) parts.push(`${ok.length} 路已绑定 ${pool.name}`)
      if (skipped.length) parts.push(`${skipped.length} 路已在该池，跳过`)
      if (failed.length) parts.push(`失败：${failed.join('；')}`)
      setResult(parts.join(' · ') || '没有需要绑定的通道')
      if (failed.length === 0) setPicked(new Set())
      await client.invalidateQueries()
    }, '绑定已执行')
  }

  const summaryText =
    bound.length === 0
      ? '未绑定通道'
      : `已绑定 ${bound.length} 路通道：${bound.map(label).join('、')}`
  const pendingPick = bindable.filter(
    (item) => picked.has(item.channel_id) && item.storage_pool_id !== pool.id
  ).length

  return (
    <div className='border-t pt-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <p className='text-sm text-muted-foreground' data-testid={`pool-binding-${pool.name}`}>
          {summaryText}
        </p>
        <Button
          type='button'
          variant='outline'
          size='sm'
          className='h-7 px-2 text-xs'
          disabled={!enabled}
          aria-expanded={open}
          onClick={() => setOpen((value) => !value)}
        >
          管理绑定通道
        </Button>
      </div>
      {result && (
        <p role='status' className='mt-1.5 text-xs text-muted-foreground'>
          {result}
        </p>
      )}
      {open && (
        <div className='mt-2 rounded-lg border bg-background p-2.5'>
          <p className='mb-2 text-xs text-muted-foreground'>
            勾选要绑定到 {pool.name} 的通道（仅列出已配置源且可配置的通道；重选已在该池的通道不会重复写）。
          </p>
          <div className='flex flex-wrap items-center gap-1.5'>
            {bindable.map((item) => {
              const id = item.channel_id
              const pickedNow = picked.has(id)
              const isBound = item.storage_pool_id === pool.id
              return (
                <button
                  key={id}
                  type='button'
                  aria-pressed={pickedNow}
                  aria-label={`绑定 ${label(item)} ${item.channel_name}`}
                  disabled={!enabled}
                  onClick={() => toggle(id, !pickedNow)}
                  className={cn(
                    'h-6 rounded-md border px-2 text-xs transition-colors',
                    pickedNow
                      ? 'border-emerald-500/40 bg-emerald-500/15 text-emerald-700 dark:text-emerald-400'
                      : isBound
                        ? 'border-emerald-500/20 bg-emerald-500/5 text-muted-foreground'
                        : 'text-muted-foreground hover:bg-accent'
                  )}
                  title={isBound ? '已绑定该池' : item.channel_name}
                >
                  {label(item)}
                  {isBound && ' ✓'}
                </button>
              )
            })}
            {bindable.length === 0 && (
              <span className='text-xs text-muted-foreground'>
                暂无可绑定的通道
              </span>
            )}
          </div>
          <div className='mt-2 flex flex-wrap items-center gap-2'>
            <Button
              type='button'
              variant='outline'
              size='sm'
              className='h-6 px-2 text-xs'
              disabled={!enabled || bindable.length === 0}
              onClick={pickConfigured}
            >
              全选已配置通道
            </Button>
            <Button
              type='button'
              variant='ghost'
              size='sm'
              className='h-6 px-2 text-xs'
              disabled={picked.size === 0}
              onClick={() => setPicked(new Set())}
            >
              清除选择
            </Button>
            <Button
              type='button'
              size='sm'
              className='h-6 px-2.5 text-xs'
              disabled={action.pending || pendingPick === 0}
              onClick={() => void bind()}
            >
              绑定所选 {pendingPick > 0 ? `(${pendingPick})` : ''}
            </Button>
          </div>
          <p className='mt-1.5 text-xs text-muted-foreground'>
            使用该池前仍需通过存储池写入检查；被拒的行会自动补一次真实检查并重试。
          </p>
        </div>
      )}
    </div>
  )
}
