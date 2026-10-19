import { describe, expect, test } from 'vitest'
import type { Schema } from '@/lib/types'
import { eventSeconds, missingSnapshotReason } from './snapshot-state'

function event(overrides: Partial<Schema<'Event'>>): Schema<'Event'> {
  return {
    id: 'e1',
    started_at: '2026-10-19T02:55:45Z',
    ended_at: '2026-10-19T02:55:55Z',
    snapshot_state: 'gone',
    ...overrides,
  }
}

describe('missingSnapshotReason', () => {
  // The live site separated cleanly at three seconds: 25% of events under that
  // had no picture, 3% between three and ten, 1% beyond.
  test('a detection shorter than the threshold reads as too short', () => {
    expect(
      missingSnapshotReason(
        event({ ended_at: '2026-10-19T02:55:46Z' })
      )
    ).toBe('too_short')
  })

  test('a longer event with no picture is a genuine absence', () => {
    expect(missingSnapshotReason(event({}))).toBe('absent')
  })

  // A three-second event is exactly at the boundary the data supported, so it
  // must not be excused as "too short".
  test('exactly at the threshold counts as a real absence', () => {
    expect(
      missingSnapshotReason(event({ ended_at: '2026-10-19T02:55:48Z' }))
    ).toBe('absent')
  })

  // Only a missing picture is classified: a pending or ready snapshot is a
  // different question, and calling it "too short" would explain away a
  // picture that is on its way.
  test('a picture that exists or may still arrive is not classified', () => {
    expect(missingSnapshotReason(event({ snapshot_state: 'ready' }))).toBe(
      'not_applicable'
    )
    expect(missingSnapshotReason(event({ snapshot_state: 'pending' }))).toBe(
      'not_applicable'
    )
  })

  // Without an end time the duration is unknown, so the honest answer is the
  // ordinary absence rather than a specific excuse.
  test('an event without an end is a plain absence', () => {
    expect(missingSnapshotReason(event({ ended_at: null }))).toBe('absent')
  })
})

describe('eventSeconds', () => {
  test('rounds to whole seconds and rejects nonsense', () => {
    expect(eventSeconds(event({}))).toBe(10)
    expect(
      eventSeconds(event({ ended_at: '2026-10-19T02:55:46Z' }))
    ).toBe(1)
    expect(eventSeconds(event({ ended_at: null }))).toBeNull()
  })
})
