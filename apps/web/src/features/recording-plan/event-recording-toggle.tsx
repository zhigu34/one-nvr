import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
import { Button } from '@/components/ui/button'
import { useAPI } from '@/features/foundation/hooks'
import { cn } from '@/lib/utils'

/**
 * The event-recording switch (PRD REC-09).
 *
 * It is shown only on a channel whose mode is `none`, because that is the only
 * place it does anything: a channel inside a continuous, scheduled or manual
 * window already writes footage, and an event links to those segments instead.
 * Offering the switch elsewhere would imply it affects ordinary recording, which
 * REC-10 explicitly forbids.
 *
 * The buffer itself needs memory reserved at deployment time. When the overview
 * reports no cache, the switch still records the operator's intent but the row
 * states that no buffer can run — an honest "on, but not possible here" rather
 * than a silent no-op.
 */
export function EventRecordingToggle({
  channelId,
  label,
  enabled,
  disabled,
}: {
  channelId: string
  label: string
  enabled: boolean
  disabled?: boolean
}) {
  const client = useQueryClient()
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const cache = useAPI<{ event_cache: { configured: boolean } }>(
    '/api/v1/operations/resources',
    true
  )
  const cacheConfigured = cache.data?.event_cache.configured ?? true

  async function toggle() {
    setPending(true)
    setError('')
    try {
      // The version comes from the live policy read, not from a page that may be
      // stale: the switch shares its row with the mode, so a concurrent edit has
      // to be detected rather than overwritten.
      const policy = await apiRequest<{ version: number }>(
        `/api/v1/channels/${channelId}/recording-policy`
      )
      await apiRequest(
        `/api/v1/channels/${channelId}/event-recording`,
        jsonRequest('PUT', { enabled: !enabled }, policy.version)
      )
      await client.invalidateQueries()
    } catch (cause) {
      setError(
        cause instanceof ApiError && cause.status === 409
          ? '配置已被其他人修改，请刷新后重试'
          : cause instanceof Error
            ? cause.message
            : '切换失败'
      )
    } finally {
      setPending(false)
    }
  }

  return (
    <span className='inline-flex shrink-0 items-center gap-1.5'>
      <Button
        type='button'
        variant='outline'
        size='sm'
        className={cn(
          'h-7 shrink-0 px-2 text-xs',
          enabled && 'border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
        )}
        disabled={pending || disabled}
        aria-label={`${label} 事件录像`}
        aria-pressed={enabled}
        onClick={() => void toggle()}
      >
        事件录像{enabled ? ' 开' : ' 关'}
      </Button>
      {enabled && !cacheConfigured && (
        <span
          className='text-xs text-amber-600 dark:text-amber-400'
          title='部署时未预留内存给事件缓冲（ONE_NVR_EVENT_CACHE_MB），因此事件到达时没有前录画面'
        >
          未预留缓存
        </span>
      )}
      {error && (
        <span role='alert' className='text-xs text-red-500'>
          {error}
        </span>
      )}
    </span>
  )
}
