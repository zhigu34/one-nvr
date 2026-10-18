import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Circle, Square } from 'lucide-react'
import type { Schema } from '@/lib/types'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
import { Button } from '@/components/ui/button'
import { useAPI } from '@/features/foundation/hooks'
import { cn } from '@/lib/utils'
import { remainingLabel } from './manual-format'

/**
 * Manual recording control for one channel (PRD REC-03).
 *
 * It is deliberately not a mode in the policy select. A manual recording is a
 * bounded request — "record for 30 minutes, starting now" — and REC-04 requires
 * it to survive a policy edit made while it runs. Putting it in the same control
 * as the stored mode would make the two compete for one value, and whichever was
 * written last would silently cancel the other.
 *
 * The remaining time is shown rather than the requested duration: "28 分钟" tells
 * an operator what is left, while "30 分钟" would be equally true at minute 1 and
 * minute 29.
 */
const DURATIONS = [10, 30, 60, 120] as const

export function ManualRecordingControl({
  channelId,
  label,
  disabled,
}: {
  channelId: string
  label: string
  disabled?: boolean
}) {
  const client = useQueryClient()
  const [open, setOpen] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  // Polled while a window is open: the countdown is the point, and a value
  // fetched once would be wrong within a minute.
  const active = useAPI<Schema<'ManualRecording'>>(
    `/api/v1/channels/${channelId}/manual-recording`,
    true,
    15_000
  )
  // 404 is the endpoint's "no window is open" answer, not a failure: treating it
  // as an error would paint an error on every idle channel. Anything else (403,
  // 500) is a real failure and must not be rendered as "not recording" — that
  // would invite the operator to press record on a channel they cannot read.
  const absent =
    active.error instanceof ApiError &&
    active.error.code === 'manual_recording_absent'
  const window = absent ? null : active.data

  async function start(minutes: number) {
    setPending(true)
    setError('')
    try {
      await apiRequest(
        `/api/v1/channels/${channelId}/manual-recording`,
        jsonRequest('POST', { minutes })
      )
      setOpen(false)
      await active.refetch()
      void client.invalidateQueries()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '无法开始手动录像')
    } finally {
      setPending(false)
    }
  }

  async function stop() {
    setPending(true)
    setError('')
    try {
      await apiRequest(`/api/v1/channels/${channelId}/manual-recording`, {
        method: 'DELETE',
      })
      await active.refetch()
      void client.invalidateQueries()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '无法停止手动录像')
    } finally {
      setPending(false)
    }
  }

  if (window) {
    return (
      <div className='flex flex-wrap items-center gap-1.5'>
        <span
          data-testid='manual-recording-active'
          className='inline-flex items-center gap-1 rounded-full border border-red-500/30 bg-red-500/10 px-2 py-0.5 text-xs text-red-500'
          title={`开始于 ${new Date(window.started_at).toLocaleTimeString('zh-CN', { hour12: false })}，共 ${window.requested_minutes} 分钟`}
        >
          <span aria-hidden className='size-1.5 animate-pulse rounded-full bg-red-500' />
          手动录像 {remainingLabel(window.remaining_ms)}
        </span>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          className='h-7 px-2 text-xs'
          disabled={pending || disabled}
          aria-label={`${label} 停止手动录像`}
          onClick={() => void stop()}
        >
          <Square aria-hidden className='size-3' />
          停止
        </Button>
        {error && (
          <span role='alert' className='text-xs text-red-500'>
            {error}
          </span>
        )}
      </div>
    )
  }

  return (
    <div className='flex flex-wrap items-center gap-1.5'>
      <Button
        type='button'
        variant='outline'
        size='sm'
        className='h-7 px-2 text-xs'
        disabled={pending || disabled}
        aria-label={`${label} 手动录像`}
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <Circle aria-hidden className='size-3 text-red-500' />
        手动录像
      </Button>
      {open && (
        <span className='inline-flex items-center gap-1 rounded-md border px-1 py-0.5'>
          {DURATIONS.map((minutes) => (
            <button
              key={minutes}
              type='button'
              disabled={pending}
              aria-label={`${label} 录像 ${minutes} 分钟`}
              className={cn(
                'rounded px-1.5 py-0.5 text-xs tabular-nums',
                'hover:bg-accent hover:text-accent-foreground'
              )}
              onClick={() => void start(minutes)}
            >
              {minutes} 分
            </button>
          ))}
        </span>
      )}
      {error && (
        <span role='alert' className='text-xs text-red-500'>
          {error}
        </span>
      )}
      {active.error && !absent && (
        <span role='alert' className='text-xs text-amber-600 dark:text-amber-400'>
          状态未知：{active.error.message}
        </span>
      )}
    </div>
  )
}
