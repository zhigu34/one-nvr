import type { Schema } from '@/lib/types'
import { STATUS_REASONS, UNKNOWN_STATUS_LABELS } from './status-reasons'

export type ObservedState = Schema<'ObservedStatus'>['state'] | 'pending'

const labels: Record<ObservedState, string> = {
  pending: '检查中',
  healthy: '正常',
  unavailable: '不可用',
  unknown: '未知',
  disabled: '已关闭',
  not_configured: '未配置',
  degraded: '降级',
}

// One severity vocabulary for every surface that shows an observation: the
// status label, the channel picker's dots, and anything added later.
export type StatusTone = 'ok' | 'warn' | 'bad' | 'muted'

export function statusTone(value: { state: ObservedState }): StatusTone {
  if (value.state === 'healthy') return 'ok'
  if (value.state === 'degraded') return 'warn'
  if (value.state === 'unavailable') return 'bad'
  return 'muted'
}

// "No observation yet" and "the observation expired" both arrive as `unknown`,
// which reads like a fault. Name them for what they are so the operator can
// tell "not tested" apart from "broken".
export function statusText(value: {
  state: ObservedState
  reason?: string
}) {
  if (value.state === 'unknown' && value.reason)
    return UNKNOWN_STATUS_LABELS[value.reason] || labels.unknown
  return labels[value.state]
}

export function statusReason(value: { reason?: string }) {
  return value.reason ? STATUS_REASONS[value.reason] || value.reason : ''
}

export const statusTextTones: Record<StatusTone, string> = {
  ok: 'text-emerald-700 dark:text-emerald-400',
  warn: 'text-amber-700 dark:text-amber-400',
  bad: 'text-destructive',
  muted: 'text-muted-foreground',
}
export const dotTones: Record<StatusTone, string> = {
  ok: 'bg-emerald-600',
  warn: 'bg-amber-500',
  bad: 'bg-destructive',
  muted: 'bg-muted-foreground/40',
}
