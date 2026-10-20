import { describe, expect, test } from 'vitest'
import { clusterMarks, eventMarks, eventWindowSize, markLabel } from './event-marks'
import type { Schema } from '@/lib/types'

const base = Date.parse('2026-10-05T00:00:00Z')
const event = (overrides: Partial<Schema<'Event'>>): Schema<'Event'> => ({
  id: 'e1',
  channel_id: 'c',
  category: 'person',
  score: 0.8,
  zones: [],
  started_at: new Date(base + 60_000).toISOString(),
  ended_at: new Date(base + 90_000).toISOString(),
  snapshot_state: 'ready',
  handle_state: 'open',
  ...overrides,
})

describe('eventMarks', () => {
  const viewStart = base
  const viewEnd = base + 3600_000

  test('places events inside the view and drops the rest', () => {
    const marks = eventMarks(
      [
        event({ id: 'in', started_at: new Date(base + 60_000).toISOString() }),
        event({ id: 'before', started_at: new Date(base - 60_000).toISOString(), ended_at: new Date(base - 30_000).toISOString() }),
        event({ id: 'after', started_at: new Date(viewEnd + 60_000).toISOString() }),
      ],
      viewStart,
      viewEnd
    )
    expect(marks.map((m) => m.id)).toEqual(['in'])
  })

  test('an event straddling the view edge is kept', () => {
    const marks = eventMarks(
      [event({ id: 'straddle', started_at: new Date(base - 60_000).toISOString(), ended_at: new Date(base + 60_000).toISOString() })],
      viewStart,
      viewEnd
    )
    expect(marks).toHaveLength(1)
  })

  test('an event without an id or a finite start never renders', () => {
    const marks = eventMarks(
      [
        event({ id: '', started_at: new Date(base + 60_000).toISOString() }),
        event({ id: 'bad', started_at: 'not-a-date' }),
      ],
      viewStart,
      viewEnd
    )
    expect(marks).toHaveLength(0)
  })

  test('marks are sorted by time regardless of input order', () => {
    const marks = eventMarks(
      [
        event({ id: 'late', started_at: new Date(base + 600_000).toISOString() }),
        event({ id: 'early', started_at: new Date(base + 60_000).toISOString() }),
      ],
      viewStart,
      viewEnd
    )
    expect(marks.map((m) => m.id)).toEqual(['early', 'late'])
  })
})

describe('eventWindowSize', () => {
  test('covers the whole view window up to now', () => {
    const now = Date.now()
    const { start, end } = eventWindowSize(now - 3 * 86_400_000, now + 1000)
    expect(end).toBeLessThanOrEqual(now + 1000)
    expect(end - start).toBeGreaterThan(3 * 86_400_000 - 60_000)
  })

  test('clamps to the events index retention', () => {
    const now = Date.now()
    const { start, end } = eventWindowSize(now - 200 * 86_400_000, now)
    expect(end - start).toBeLessThanOrEqual(90 * 86_400_000)
  })
})

describe('clusterMarks', () => {
  // Offsets are milliseconds from `base`, matching the 1000ms window below.
  const marks = (offsetsMs: number[]) =>
    offsetsMs.map((offset, index) => ({
      id: `e${index}`,
      at: base + offset,
      end: base + offset + 1,
      category: 'person',
      score: 0.5 + index / 100,
      handleState: 'open',
    }))

  test('marks within the pixel minimum fold into one cluster', () => {
    // 1000ms span on 100px: 1ms == 0.1px, so 4ms apart (0.4px) folds at min 5px.
    const clustered = clusterMarks(marks([0, 4]), base, base + 1000, 100, 5)
    expect(clustered).toHaveLength(1)
    expect(clustered[0].count).toBe(2)
  })

  test('marks far enough apart stay separate', () => {
    const clustered = clusterMarks(marks([0, 500]), base, base + 1000, 100, 5)
    expect(clustered).toHaveLength(2)
  })

  test('a cluster keeps the highest score and the widest end', () => {
    const clustered = clusterMarks(
      [
        { id: 'a', at: base, end: base + 10, category: 'car', score: 0.9, handleState: 'open' },
        { id: 'b', at: base + 1, end: base + 12, category: 'person', score: 0.6, handleState: 'open' },
      ],
      base,
      base + 1000,
      100,
      5
    )
    expect(clustered).toHaveLength(1)
    expect(clustered[0].category).toBe('car')
    expect(clustered[0].end).toBe(base + 12)
  })
})

describe('markLabel', () => {
  test('known categories translate, unknown degrade visibly', () => {
    expect(markLabel('person')).toBe('人')
    expect(markLabel('squirrel')).toBe('squirrel')
    expect(markLabel('')).toBe('事件')
  })
})


// The event track answers "when did something happen", so a mark's drawn
// appearance must not depend on its worklist state. Handle state lives on the
// events page; putting it on a time axis made an operator wonder whether a
// hollow block meant a different kind of event.
test('a mark carries no drawing-relevant state beyond time and category', () => {
  const [mark] = eventMarks(
    [
      {
        id: 'e1',
        channel_id: 'c',
        source: 'frigate',
        source_id: 's1',
        category: 'person',
        score: 0.9,
        zones: [],
        started_at: '2026-10-19T10:00:00Z',
        ended_at: '2026-10-19T10:00:30Z',
        snapshot_state: 'ready',
        media_state: 'linked',
        handle_state: 'handled',
        version: 1,
        handling: [],
      } as never,
    ],
    Date.parse('2026-10-19T09:00:00Z'),
    Date.parse('2026-10-19T11:00:00Z')
  )
  expect(mark.at).toBe(Date.parse('2026-10-19T10:00:00Z'))
  expect(mark.end).toBe(Date.parse('2026-10-19T10:00:30Z'))
  expect(mark.category).toBe('person')
})
