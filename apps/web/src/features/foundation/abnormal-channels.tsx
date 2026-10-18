import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { AlertTriangle, ArrowRight } from 'lucide-react'
import type { Schema } from '@/lib/types'
import { useAPI } from '@/features/foundation/hooks'
import { KIND_LABEL, durationLabel, windowFor } from '@/features/channels/reliability-format'

/**
 * Overview card: which channels are abnormal right now (PRD REL-05).
 *
 * It lists problems rather than rates. The overview is what an operator patrols,
 * and a percentage invites comparison while a list of names invites action.
 *
 * Only channels with enough observation are listed, for the same reason the
 * operations comparison marks insufficient samples: a channel observed for five
 * minutes out of a day having an outage is not evidence that it is the worst
 * camera, and putting it at the top of the overview would train the operator to
 * ignore this card.
 */
export function AbnormalChannels() {
  // A day back: long enough that an overnight outage is still visible, short
  // enough that this stays about the present.
  const [window] = useState(() => windowFor(24))
  const summary = useAPI<{ items: Schema<'ReliabilityChannelSummary'>[] }>(
    `/api/v1/reliability/summary?from=${window.from}&to=${window.to}`
  )
  // The overview is polled, so this must not be a heavy query. The daily rollup
  // behind it is a bounded read, and it is the same request the operations page
  // makes — one definition of "abnormal" rather than two.

  const abnormal = useMemo(() => {
    const items = (summary.data?.items || []).filter((row) => row.kind === 'main')
    return items
      .filter((row) => row.sample_sufficient)
      .filter((row) => row.unavailable_ms > 0 || row.fault_count > 0)
      .sort((a, b) => b.unavailable_ms - a.unavailable_ms)
      .slice(0, 6)
  }, [summary.data])

  // Judged by the backend's own sufficiency flag rather than by re-deriving it
  // from a note string: a scope note ("only 14h of this week was observed") is
  // not the same as insufficiency, and treating it as one would hide a channel
  // that has plenty of evidence.
  const unjudged = useMemo(
    () =>
      (summary.data?.items || []).filter(
        (row) => row.kind === 'main' && !row.sample_sufficient
      ).length,
    [summary.data]
  )

  // A failed read hides the card rather than showing "0 异常": claiming the site
  // is healthy when the evidence is missing is the one thing this card must not
  // do.
  if (summary.error) return null
  if (summary.isPending) {
    return (
      <section className='rounded-xl border bg-card p-4' aria-label='异常通道'>
        <p className='text-xs text-muted-foreground'>异常通道 · 正在加载…</p>
      </section>
    )
  }

  return (
    <section className='rounded-xl border bg-card p-4' data-testid='abnormal-channels'>
      <div className='flex items-center justify-between'>
        <p className='flex items-center gap-1.5 text-xs text-muted-foreground'>
          <AlertTriangle aria-hidden className='size-3.5' />
          异常通道 · 最近 24 小时
        </p>
        <Link to='/operations' className='text-xs underline'>
          运维页比较
        </Link>
      </div>
      {abnormal.length === 0 ? (
        <p className='mt-2 text-sm text-muted-foreground'>
          有观测的通道没有出现中断。
        </p>
      ) : (
        /* Two columns: a full-width row put the channel name at the far left and
           the outage figure at the far right, a thousand pixels apart, which is
           the one thing a scanned list must not do. */
        <ul className='mt-2 grid gap-1.5 md:grid-cols-2'>
          {abnormal.map((row) => (
            <li
              key={row.channel_id}
              className='flex items-center justify-between gap-2 rounded-lg border border-red-500/20 bg-red-500/5 px-2 py-1.5 text-sm'
            >
              <Link
                to='/channels/configure'
                search={{ channel: row.channel_id }}
                className='group inline-flex items-center gap-1.5 hover:underline'
              >
                <span className='font-mono text-xs text-muted-foreground'>
                  CH{String(row.channel_no).padStart(2, '0')}
                </span>
                <span>{row.channel_name}</span>
                <ArrowRight aria-hidden className='size-3 opacity-0 group-hover:opacity-100' />
              </Link>
              <span className='flex shrink-0 items-center gap-1.5 text-xs'>
                <span className='rounded-full bg-red-500/10 px-1.5 py-0.5 tabular-nums text-red-600 dark:text-red-400'>
                  中断 {durationLabel(row.unavailable_ms)}
                </span>
                {row.fault_count > 0 && (
                  <span className='rounded-full bg-muted px-1.5 py-0.5 tabular-nums text-muted-foreground'>
                    {row.fault_count} 次
                  </span>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
      {unjudged > 0 && (
        <p className='mt-2 text-xs text-muted-foreground'>
          另有 {unjudged} 路观察时长不足，未纳入判断（{KIND_LABEL.main}）。
        </p>
      )}
    </section>
  )
}

