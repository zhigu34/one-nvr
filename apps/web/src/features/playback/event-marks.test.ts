import { describe, expect, test } from 'vitest'
import { eventMarks, eventWindowSize, markLabel, wouldCollide } from './event-marks'
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
  test('clamps the query window to a day ending now', () => {
    const now = Date.now()
    const { start, end } = eventWindowSize(now - 30 * 86_400_000, now + 1000)
    expect(end - start).toBeLessThanOrEqual(86_400_000)
    expect(end).toBeLessThanOrEqual(now + 1000)
  })
})

describe('wouldCollide', () => {
  test('two marks closer than the minimum pixel width collide', () => {
    const from = 0
    const span = 1000
    expect(wouldCollide({ at: 0 }, { at: 4 }, from, span, 100, 5)).toBe(true)
    expect(wouldCollide({ at: 0 }, { at: 100 }, from, span, 100, 5)).toBe(false)
  })
})

describe('markLabel', () => {
  test('known categories translate, unknown degrade visibly', () => {
    expect(markLabel('person')).toBe('人')
    expect(markLabel('squirrel')).toBe('squirrel')
    expect(markLabel('')).toBe('事件')
  })
})
