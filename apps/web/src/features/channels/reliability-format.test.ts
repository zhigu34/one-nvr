import { describe, expect, test } from 'vitest'
import {
  bandReason,
  bandTone,
  channelLabel,
  emptyObservationReason,
  formatDuration,
  percentage,
  precisionNote,
  rateEvidence,
  sampleNote,
  windowFor,
  type Rate,
  type TimeBand,
} from './reliability-format'

const rate = (over: Partial<Rate> = {}): Rate => ({
  status: 'ok',
  numerator_ms: 40_000,
  denominator_ms: 50_000,
  unjudgeable_ms: 0,
  ...over,
})

describe('percentage', () => {
  // The PRD's own worked example: 40 minutes up of 50 known is 80%.
  test('computes the ratio from its inputs', () => {
    expect(percentage(rate())).toEqual({ kind: 'percent', value: '80.00%' })
  })

  // A missing value must never render as 0%: "nothing was observed" and
  // "everything was down" are opposite conclusions.
  test.each([
    ['no_observation', '无有效观测'],
    ['not_applicable', '不适用'],
    ['no_samples', '无样本'],
  ] as const)('renders %s as its reason rather than a number', (status, label) => {
    const result = percentage(rate({ status, numerator_ms: 0, denominator_ms: 0 }))
    expect(result).toEqual({ kind: 'reason', label })
  })

  test('a zero denominator is reported as a reason even when the status claims ok', () => {
    const result = percentage(rate({ numerator_ms: 0, denominator_ms: 0 }))
    expect(result.kind).toBe('reason')
  })
})

describe('formatDuration', () => {
  test.each([
    [0, '0 秒'],
    [30_000, '30 秒'],
    [90_000, '1 分 30 秒'],
    [600_000, '10 分'],
    [3_600_000, '1 小时'],
    [5_400_000, '1 小时 30 分'],
    [86_400_000, '1 天'],
    [180_000_000, '2 天 2 小时'],
  ])('formats %i ms as %s', (ms, want) => {
    expect(formatDuration(ms)).toBe(want)
  })
})

describe('rateEvidence', () => {
  // REL-04: every ratio travels with its numerator, denominator and
  // unjudgeable time so a reader can recompute it.
  test('names the numerator and denominator', () => {
    expect(rateEvidence(rate())).toBe('分子 40 秒 · 分母 50 秒')
  })

  // Unjudgeable time is one fact about the window, shown once in the time
  // accounting. Repeating it under every ratio made it look like each ratio had
  // its own and pushed the differing numbers out of view.
  test('does not repeat the window-level unjudgeable time per rate', () => {
    expect(rateEvidence(rate({ unjudgeable_ms: 10_000 }))).not.toContain('不可判定')
    expect(rateEvidence(rate())).not.toContain('不可判定')
  })
})

describe('precisionNote', () => {
  test('says nothing when the window is exact', () => {
    expect(precisionNote('exact')).toBe('')
    expect(precisionNote(undefined)).toBe('')
  })

  // REL-06: crossing into a summary tier must be stated, not implied.
  test('explains a degraded tier', () => {
    expect(precisionNote('minute_rollup')).toContain('分钟汇总')
    expect(precisionNote('daily_rollup')).toContain('按日汇总')
    expect(precisionNote('unavailable')).toContain('无证据')
  })
})

describe('bandTone', () => {
  test('colours the four states apart', () => {
    const healthy = bandTone({ kind: 'main', state: 'healthy', reason: 'r', started_at: '' })
    const unavailable = bandTone({ kind: 'main', state: 'unavailable', reason: 'r', started_at: '' })
    const unknown = bandTone({ kind: 'main', state: 'unknown', reason: 'r', started_at: '' })
    expect(new Set([healthy, unavailable, unknown]).size).toBe(3)
  })

  // Excluded time is not good news, so it must not be green: a disabled channel
  // would otherwise read as perfectly available.
  test('excluded time is grey, never green', () => {
    const excluded: TimeBand = {
      kind: 'recording',
      state: 'excluded',
      exclusion: 'disabled',
      reason: 'recording_disabled',
      started_at: '',
    }
    expect(bandTone(excluded)).not.toContain('emerald')
    expect(bandTone({ ...excluded, state: 'healthy' })).not.toContain('emerald')
  })
})

describe('bandReason', () => {
  test('prefers the exclusion explanation when one is set', () => {
    expect(
      bandReason({
        kind: 'recording',
        state: 'excluded',
        exclusion: 'disabled',
        reason: 'recording_disabled',
        started_at: '',
      })
    ).toBe('已停用或明确关闭')
  })

  test('falls back to the reason code for ordinary states', () => {
    expect(
      bandReason({ kind: 'main', state: 'unavailable', reason: 'source_unavailable', started_at: '' })
    ).toBe('source_unavailable')
  })
})

describe('sampleNote', () => {
  // PRD REL-05: too little history means "insufficient sample", not "most
  // reliable".
  test('warns when the window holds almost no observation', () => {
    expect(sampleNote(60_000, 86_400_000)).toContain('样本不足')
  })

  // The floor is a duration, not a fraction of the window. This is the live-site
  // case: a site up for fourteen hours compared over seven days has 8% coverage,
  // and the percentage rule labelled every channel insufficient even though
  // fourteen hours is plenty of evidence.
  test('fourteen hours of observation is enough, however long the window', () => {
    const note = sampleNote(14 * 3600_000, 7 * 86_400_000)
    // Never "insufficient sample": fourteen hours is real evidence.
    expect(note).not.toContain('样本不足')
    // But the reduced scope IS disclosed, because the ratios then cover far less
    // time than the window header implies.
    expect(note).toContain('仅')
    expect(note).toContain('有观测')
  })

  // Zero observation is a different fact from a small sample: it usually means
  // nothing was required to run (recording off), not that the camera is bad.
  test('zero observation is not described as an insufficient sample', () => {
    const note = sampleNote(0, 86_400_000)
    expect(note).not.toContain('样本不足')
    expect(note).toContain('没有观测记录')
  })

  test('says nothing when the evidence is substantial', () => {
    expect(sampleNote(50_000_000, 86_400_000)).toBe('')
  })
})

describe('emptyObservationReason', () => {
  // On a default install every channel has recording off, so every recording row
  // has no observation. Saying "insufficient sample" there would imply the
  // camera is unreliable rather than switched off.
  test('names the reason per kind', () => {
    expect(emptyObservationReason('recording')).toContain('未要求录像')
    expect(emptyObservationReason('main')).not.toContain('未要求录像')
  })
})

describe('channelLabel', () => {
  // The fault table used to interpolate channel_no directly, so a response that
  // omitted the field rendered "CHundefined" on every row — which looks like a
  // channel literally named "undefined" rather than a missing value.
  test('pads a real channel number', () => {
    expect(channelLabel(1)).toBe('CH01')
    expect(channelLabel(8)).toBe('CH08')
    expect(channelLabel(32)).toBe('CH32')
  })

  test('never prints undefined', () => {
    for (const value of [undefined, null, Number.NaN, 0, -1]) {
      const label = channelLabel(value as number | null | undefined)
      expect(label).toBe('CH—')
      expect(label).not.toContain('undefined')
      expect(label).not.toContain('NaN')
    }
  })
})

describe('windowFor', () => {
  test('produces a window ending now and starting the requested time back', () => {
    const now = new Date('2026-10-17T12:00:00Z')
    const { from, to } = windowFor(24, now)
    expect(to).toBe('2026-10-17T12:00:00.000Z')
    expect(from).toBe('2026-10-16T12:00:00.000Z')
  })
})
