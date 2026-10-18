import { describe, expect, it } from 'vitest'
import { weekPill, dayWindowsLabel } from './mode-format'
import type { ScheduleWindow } from './schedule-format'

const window = (weekday: number, start: number, end: number): ScheduleWindow => ({
  weekday,
  start_minute: start,
  end_minute: end,
})

describe('weekPill', () => {
  it('marks the planned days and says 多时段 when ranges differ', () => {
    const result = weekPill([window(1, 480, 1080), window(7, 600, 660)])
    expect([...result.days]).toEqual([1, 7])
    expect(result.text).toBe('多时段')
  })

  it('shows the bare time range for a single uniform window', () => {
    const result = weekPill([window(7, 480, 540)])
    expect([...result.days]).toEqual([7])
    expect(result.text).toBe('08:00 – 09:00')
  })

  it('shows the bare range when every day records the same window', () => {
    const week = [1, 2, 3, 4, 5, 6, 7].map((day) => window(day, 0, 1440))
    const result = weekPill(week)
    expect(result.days.size).toBe(7)
    expect(result.text).toBe('00:00 – 00:00')
  })

  it('shows the range for a working-day week without weekday names', () => {
    const week = [1, 2, 3, 4, 5].map((day) => window(day, 480, 1080))
    const result = weekPill(week)
    expect(result.text).toBe('08:00 – 18:00')
  })

  it('keeps text empty for an empty week', () => {
    const result = weekPill([])
    expect(result.days.size).toBe(0)
    expect(result.text).toBe('')
  })

  it('says 多时段 when one day carries two different windows', () => {
    const result = weekPill([window(1, 480, 720), window(1, 780, 1080)])
    expect(result.text).toBe('多时段')
  })
})

describe('dayWindowsLabel', () => {
  it('names the day and its windows', () => {
    expect(dayWindowsLabel([window(7, 480, 540)], 7)).toBe('周日 08:00 – 09:00')
  })

  it('says 不录 for a day without windows', () => {
    expect(dayWindowsLabel([], 3)).toBe('周三 不录')
  })

  it('joins multiple windows on one day', () => {
    expect(dayWindowsLabel([window(1, 480, 720), window(1, 780, 1080)], 1)).toBe(
      '周一 08:00 – 12:00、13:00 – 18:00'
    )
  })
})
