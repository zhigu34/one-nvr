// Site-timezone wall-clock helpers. Records are stored in UTC, but every
// operator-facing value — the search range, time-axis labels and the playback
// position — is expressed in the site's IANA zone, so both directions of that
// conversion live here and nowhere else.

export type ZoneParts = {
  year: number
  month: number
  day: number
  hour: number
  minute: number
  second: number
}

const formatters = new Map<string, Intl.DateTimeFormat>()
const pad = (value: number, width = 2) => String(value).padStart(width, '0')

function formatter(timeZone: string) {
  const cached = formatters.get(timeZone)
  if (cached) return cached
  const created = new Intl.DateTimeFormat('en-US', {
    timeZone,
    hourCycle: 'h23',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
  formatters.set(timeZone, created)
  return created
}

/** Wall-clock fields of an instant as that zone reads them. */
export function zoneParts(epochMs: number, timeZone: string): ZoneParts {
  const parts = formatter(timeZone).formatToParts(new Date(epochMs))
  const pick = (type: Intl.DateTimeFormatPartTypes) =>
    Number(parts.find((part) => part.type === type)?.value || '0')
  return {
    year: pick('year'),
    month: pick('month'),
    day: pick('day'),
    hour: pick('hour'),
    minute: pick('minute'),
    second: pick('second'),
  }
}

/** Minutes east of UTC for that zone at that instant. */
function offsetMinutes(epochMs: number, timeZone: string): number {
  const parts = zoneParts(epochMs, timeZone)
  const asUTC = Date.UTC(
    parts.year,
    parts.month - 1,
    parts.day,
    parts.hour,
    parts.minute,
    parts.second
  )
  return (asUTC - Math.floor(epochMs / 1000) * 1000) / 60000
}

/**
 * The value an `input[type=datetime-local]` holds. Browsers drop a zero seconds
 * component, so the shortest form is produced here too: feeding the control a
 * longer string than it keeps would leave the DOM and the form state disagreeing.
 */
export function wallClockInput(epochMs: number, timeZone: string): string {
  if (!Number.isFinite(epochMs)) return ''
  const parts = zoneParts(epochMs, timeZone)
  const minutePart = `${pad(parts.year, 4)}-${pad(parts.month)}-${pad(parts.day)}T${pad(parts.hour)}:${pad(parts.minute)}`
  return parts.second === 0 ? minutePart : `${minutePart}:${pad(parts.second)}`
}

const WALL_CLOCK = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/

/**
 * Reads a datetime-local value as wall clock in the site zone, and returns NaN
 * for anything that is not a real local instant: malformed input, an impossible
 * date such as 31 February, or a time a DST jump skipped over. The browser zone
 * is never consulted, so a laptop set to another zone still searches the times
 * the site actually recorded.
 */
export function parseWallClock(value: string, timeZone: string): number {
  const match = WALL_CLOCK.exec(value)
  if (!match) return Number.NaN
  const [, year, month, day, hour, minute, second = '00'] = match
  const fields = {
    year: Number(year),
    month: Number(month),
    day: Number(day),
    hour: Number(hour),
    minute: Number(minute),
    second: Number(second),
  }
  if (
    fields.month < 1 ||
    fields.month > 12 ||
    fields.day < 1 ||
    fields.day > 31 ||
    fields.hour > 23 ||
    fields.minute > 59 ||
    fields.second > 59
  )
    return Number.NaN
  const wall = Date.UTC(
    fields.year,
    fields.month - 1,
    fields.day,
    fields.hour,
    fields.minute,
    fields.second
  )
  // One refinement step is enough for every real zone: the first guess applies
  // the offset in force at the value read as UTC, which can only be wrong across
  // a DST change.
  const first = wall - offsetMinutes(wall, timeZone) * 60000
  const resolved = wall - offsetMinutes(first, timeZone) * 60000
  const minutePart = `${pad(fields.year, 4)}-${pad(fields.month)}-${pad(fields.day)}T${pad(fields.hour)}:${pad(fields.minute)}`
  const expected =
    fields.second === 0 ? minutePart : `${minutePart}:${pad(fields.second)}`
  return wallClockInput(resolved, timeZone) === expected ? resolved : Number.NaN
}

/** 00:00:00 of the local day that contains the instant. */
export function startOfZoneDay(epochMs: number, timeZone: string): number {
  const parts = zoneParts(epochMs, timeZone)
  return parseWallClock(
    `${pad(parts.year, 4)}-${pad(parts.month)}-${pad(parts.day)}T00:00:00`,
    timeZone
  )
}

/** Absolute display format: YYYY-MM-DD HH:mm:ss in the site zone. */
export function formatClock(epochMs: number, timeZone: string): string {
  if (!Number.isFinite(epochMs)) return '—'
  const parts = zoneParts(epochMs, timeZone)
  return `${pad(parts.year, 4)}-${pad(parts.month)}-${pad(parts.day)} ${pad(parts.hour)}:${pad(parts.minute)}:${pad(parts.second)}`
}

/**
 * List and status format: YYYY-MM-DD HH:mm in the site zone. Seconds are noise
 * in a table, and the project convention is minute precision for anything that
 * is not a playback position.
 */
export function formatMinute(epochMs: number, timeZone: string): string {
  if (!Number.isFinite(epochMs)) return '—'
  const parts = zoneParts(epochMs, timeZone)
  return `${pad(parts.year, 4)}-${pad(parts.month)}-${pad(parts.day)} ${pad(parts.hour)}:${pad(parts.minute)}`
}

export type QuickRangeKind = 'hour' | 'day' | 'today' | 'yesterday'

const HOUR = 3600_000

/**
 * The ranges operators actually ask for, resolved against the site clock rather
 * than the browser one. Today and yesterday are whole local days; a zone with no
 * midnight on a given date (a DST jump can skip it) falls back to the trailing
 * 24 hours instead of reporting a range that cannot be built.
 */
export function quickRange(
  kind: QuickRangeKind,
  now: number,
  timeZone: string
): { start: number; end: number } {
  if (kind === 'hour') return { start: now - HOUR, end: now }
  if (kind === 'day') return { start: now - 24 * HOUR, end: now }
  const today = startOfZoneDay(now, timeZone)
  if (!Number.isFinite(today)) return { start: now - 24 * HOUR, end: now }
  if (kind === 'today') return { start: today, end: now }
  const yesterday = startOfZoneDay(today - HOUR, timeZone)
  return {
    start: Number.isFinite(yesterday) ? yesterday : today - 24 * HOUR,
    end: today,
  }
}
