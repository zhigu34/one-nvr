import { describe, expect, test } from 'vitest'
import { remainingLabel } from './manual-format'

describe('remainingLabel', () => {
  // A countdown must never promise more time than exists, so it rounds down:
  // 40 seconds left is "40 秒", not "1 分钟".
  test('rounds down so a window never over-reports', () => {
    expect(remainingLabel(59_999)).toBe('59 秒')
    expect(remainingLabel(60_000)).toBe('1 分钟')
    expect(remainingLabel(119_000)).toBe('1 分钟')
  })

  test('switches to hours past sixty minutes', () => {
    expect(remainingLabel(3_600_000)).toBe('1 小时')
    expect(remainingLabel(3_600_000 + 600_000)).toBe('1 小时 10 分')
  })

  test('reports an ended window rather than a negative duration', () => {
    expect(remainingLabel(0)).toBe('已结束')
    expect(remainingLabel(-5_000)).toBe('已结束')
    expect(remainingLabel(Number.NaN)).toBe('已结束')
  })
})
