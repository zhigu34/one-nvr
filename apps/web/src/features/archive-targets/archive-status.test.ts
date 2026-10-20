import { expect, test } from 'vitest'
import {
  bytesLabel,
  coverage,
  errorText,
  pendingAge,
  uploadState,
  type TargetStatus,
} from './archive-status'

function status(overrides: Partial<TargetStatus> = {}): TargetStatus {
  return { verified: 0, uploading: 0, failed: 0, verified_bytes: 0, ...overrides }
}

test('an idle target is not evidence that uploading works', () => {
  // Zero of everything means nothing has been tried yet. Calling that "正常"
  // would claim a working path from no samples at all.
  expect(uploadState(status())).toEqual({ kind: 'idle' })
  expect(uploadState(undefined)).toEqual({ kind: 'idle' })
})

test('failures outrank an idle archive, and uploading outranks failures', () => {
  expect(uploadState(status({ failed: 3, last_error: 'upload_failed' }))).toEqual({
    kind: 'failing',
    count: 3,
    code: 'upload_failed',
  })
  // A retry in flight means the archive is moving; the failures are still
  // listed, but the headline should not read as stopped.
  expect(uploadState(status({ uploading: 2, failed: 3 }))).toEqual({
    kind: 'uploading',
    count: 2,
  })
  expect(uploadState(status({ verified: 12 }))).toEqual({ kind: 'healthy' })
})

// A stopped copy is still marked 'uploading' in the table: its job ran out of
// attempts and left the row behind. Counting it as progress is how an archive
// looks busy for a week while nothing moves.
test('a copy whose job gave up is a failure, not progress', () => {
  expect(uploadState(status({ stopped: 5 }))).toEqual({
    kind: 'failing',
    count: 5,
    code: '',
  })
  expect(uploadState(status({ stopped: 2, failed: 1 }))).toEqual({
    kind: 'failing',
    count: 3,
    code: '',
  })
})

test('a failure keeps its own code instead of a friendly invention', () => {
  expect(errorText('upload_failed')).toBe('目标无法写入，或连接中断')
  expect(errorText('target_unauthorized')).toBe('目标拒绝当前凭据')
  // An unknown code is the only clue left; showing it beats showing nothing.
  expect(errorText('some_new_code')).toBe('some_new_code')
  expect(errorText('')).toBe('原因未知')
})

test('bytes are labelled in a unit an operator can compare', () => {
  expect(bytesLabel(0)).toBe('0 B')
  expect(bytesLabel(undefined)).toBe('0 B')
  expect(bytesLabel(2048)).toBe('2 KiB')
  expect(bytesLabel(5 * 1024 * 1024)).toBe('5.0 MiB')
  expect(bytesLabel(3.25 * 1024 * 1024 * 1024)).toBe('3.25 GiB')
})

test('an empty queue has no age rather than an age of zero', () => {
  const now = Date.parse('2026-10-20T12:00:00Z')
  expect(pendingAge(null, now)).toBeNull()
  expect(pendingAge(undefined, now)).toBeNull()
  // Just enqueued is distinct from "waiting minutes".
  expect(pendingAge('2026-10-20T11:59:40Z', now)).toBe('刚刚入队')
  expect(pendingAge('2026-10-20T11:30:00Z', now)).toBe('最早一项等待 30 分钟')
  expect(pendingAge('2026-10-20T09:00:00Z', now)).toBe('最早一项等待 3 小时')
})

test('a channel with nothing to archive reports no percentage', () => {
  // 0/0 is not 0%: a channel with no ready segments has nothing to cover.
  expect(coverage(0, 0)).toBeNull()
  expect(coverage(undefined, undefined)).toBeNull()
  expect(coverage(3, 4)).toEqual({ percent: 75, copied: 3, segments: 4 })
  // Copied can exceed the current ready count when segments were already
  // deleted locally after a verified upload; the bar must not exceed full.
  expect(coverage(9, 4)).toEqual({ percent: 100, copied: 4, segments: 4 })
})
