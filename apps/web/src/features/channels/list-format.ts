import type { Schema } from '@/lib/types'

/**
 * Channel list presentation helpers.
 *
 * These live outside the page so the rules that decide what an operator sees —
 * which rows collapse, which checkbox is disabled, what a timestamp reads — are
 * pinned by tests instead of being implicit in JSX.
 */

export type Channel = Schema<'ChannelSummary'>

/**
 * A channel with no applied camera source. The summary only fills source_ip
 * from the applied revision, so the two agree; the explicit state check keeps
 * old cached payloads honest.
 */
export function isUnconfigured(channel: Channel): boolean {
  return (
    !channel.source_ip &&
    !channel.current_revision_id &&
    channel.main.state === 'not_configured'
  )
}

/**
 * Collapse the trailing run of unconfigured channels.
 *
 * A 32-channel site with eight empty slots spends a third of the table on rows
 * that say nothing. The collapsed form names the range so the operator can tell
 * what is hidden, and it only applies to a contiguous run at the end — an
 * unconfigured slot in the middle is a fact about that slot, not filler.
 */
export interface CollapsedTail {
  before: Channel[]
  hidden: Channel[]
  /** e.g. "CH09–CH16", or the single channel when only one is hidden. */
  rangeLabel: string
  /** Collapse is worth offering only when it hides more than a row or two. */
  collapsed: boolean
}

export function collapseUnconfiguredTail(
  rows: Channel[],
  options: { threshold?: number } = {}
): CollapsedTail {
  const threshold = options.threshold ?? 3
  let start = rows.length
  while (start > 0 && isUnconfigured(rows[start - 1])) start--
  const hidden = rows.slice(start)
  const before = rows.slice(0, start)
  return {
    before,
    hidden,
    rangeLabel: channelRangeLabel(hidden),
    collapsed: hidden.length >= threshold,
  }
}

/** channelRangeLabel renders "CH09–CH16" for a run, or one label for a single. */
export function channelRangeLabel(channels: { channel_no: number }[]): string {
  if (channels.length === 0) return ''
  const pad = (no: number) => `CH${String(no).padStart(2, '0')}`
  if (channels.length === 1) return pad(channels[0].channel_no)
  const first = channels[0].channel_no
  const last = channels[channels.length - 1].channel_no
  return `${pad(first)}–${pad(last)}`
}

/**
 * shortStamp renders a timestamp without the current year.
 *
 * Every row in this table was updated in the current year, so repeating "2026"
 * on all of them costs four characters of a narrow column and adds nothing. The
 * full timestamp stays available in the title.
 */
export function shortStamp(
  epochMs: number,
  timeZone: string,
  now = Date.now()
): string {
  if (!Number.isFinite(epochMs) || !timeZone) return '—'
  const parts = zoneParts(epochMs, timeZone)
  if (!parts) return '—'
  const currentYear = zoneParts(now, timeZone)?.year
  const md = `${pad(parts.month)}-${pad(parts.day)} ${pad(parts.hour)}:${pad(parts.minute)}`
  return parts.year === currentYear ? md : `${parts.year}-${md}`
}

/** fullStamp is the title text: the complete local timestamp. */
export function fullStamp(epochMs: number, timeZone: string): string {
  if (!Number.isFinite(epochMs) || !timeZone) return ''
  const parts = zoneParts(epochMs, timeZone)
  if (!parts) return ''
  return `${pad(parts.year, 4)}-${pad(parts.month)}-${pad(parts.day)} ${pad(parts.hour)}:${pad(parts.minute)}`
}

function pad(value: number, width = 2): string {
  return String(value).padStart(width, '0')
}

interface ZoneParts {
  year: number
  month: number
  day: number
  hour: number
  minute: number
}

function zoneParts(epochMs: number, timeZone: string): ZoneParts | null {
  try {
    const formatter = new Intl.DateTimeFormat('en-CA', {
      timeZone,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    })
    const out: Record<string, number> = {}
    for (const part of formatter.formatToParts(epochMs)) {
      if (part.type !== 'literal') out[part.type] = Number(part.value)
    }
    if (!Number.isFinite(out.year)) return null
    return {
      year: out.year,
      month: out.month,
      day: out.day,
      hour: out.hour % 24,
      minute: out.minute,
    }
  } catch {
    return null
  }
}

/**
 * streamSummary is the hover text for a camera cell.
 *
 * Several channels can share one camera IP: a 16-channel NVR exposes 101, 201,
 * 301 … on the same address. The table shows only the IP, so the paths are the
 * only thing that tells those rows apart — shown on hover rather than in the
 * column, which would be far too wide.
 */
export function streamSummary(channel: Channel): string {
  const parts: string[] = []
  if (channel.source_ip) parts.push(channel.source_ip)
  if (channel.main_path) parts.push(`主: ${channel.main_path}`)
  if (channel.sub_path) parts.push(`子: ${channel.sub_path}`)
  if (channel.onvif_port != null) parts.push(`ONVIF 端口: ${channel.onvif_port}`)
  return parts.join('\n')
}

/**
 * placeholderFor picks the empty-cell marker.
 *
 * An unconfigured channel has no observation, no rate and no error, and every
 * one of those cells reads "—". Printing "无" for the error column made that one
 * cell look like a measured result ("no errors") when the truth is that nothing
 * has been configured yet.
 */
export function placeholderFor(
  channel: Channel,
  value: string | null | undefined,
  emptyLabel = '无'
): string {
  if (isUnconfigured(channel)) return '—'
  return value && value !== '' ? value : emptyLabel
}
