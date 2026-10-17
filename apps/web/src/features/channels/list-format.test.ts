import { describe, expect, test } from 'vitest'
import {
  channelRangeLabel,
  collapseUnconfiguredTail,
  fullStamp,
  isUnconfigured,
  placeholderFor,
  shortStamp,
  streamSummary,
  type Channel,
} from './list-format'

function channel(overrides: Partial<Channel> = {}): Channel {
  return {
    channel_id: 'ch-1',
    channel_no: 1,
    channel_name: '前门',
    channel_group: '',
    enabled: true,
    version: 1,
    permissions: ['live', 'playback', 'configure'],
    current_revision_id: 'rev-1',
    storage_pool_id: null,
    recording_mode: 'continuous',
    source_ip: '192.168.66.110',
    main_path: '/Streaming/Channels/101',
    sub_path: '/Streaming/Channels/102',
    main: { state: 'healthy', reason: 'ok', observed_at: null, expires_at: null },
    sub: { state: 'healthy', reason: 'ok', observed_at: null, expires_at: null },
    recording: {
      state: 'healthy',
      reason: 'ok',
      observed_at: null,
      expires_at: null,
    },
    last_error: '',
    bitrate_kbps: 244,
    updated_at: '2026-10-18T00:19:00Z',
    ...overrides,
  } as Channel
}

function blank(no: number): Channel {
  return channel({
    channel_id: `ch-${no}`,
    channel_no: no,
    channel_name: `通道 ${no}`,
    current_revision_id: null,
    source_ip: '',
    main_path: '',
    sub_path: '',
    main: {
      state: 'not_configured',
      reason: 'no_source',
      observed_at: null,
      expires_at: null,
    },
    last_error: '',
    bitrate_kbps: null,
  })
}

describe('isUnconfigured', () => {
  test('is true only when there is no applied source', () => {
    expect(isUnconfigured(blank(9))).toBe(true)
    expect(isUnconfigured(channel())).toBe(false)
  })
})

describe('collapseUnconfiguredTail', () => {
  // Eight empty slots at the bottom of a 32-channel site take a third of the
  // table and say nothing.
  test('collapses a trailing run of unconfigured channels', () => {
    const rows = [channel({ channel_no: 1 }), blank(9), blank(10), blank(11)]
    const tail = collapseUnconfiguredTail(rows)
    expect(tail.collapsed).toBe(true)
    expect(tail.before).toHaveLength(1)
    expect(tail.hidden).toHaveLength(3)
    expect(tail.rangeLabel).toBe('CH09–CH11')
  })

  // An empty slot in the middle is a fact about that slot, not filler at the
  // bottom; collapsing it would hide a row the operator may be looking for.
  test('does not collapse a gap in the middle of the list', () => {
    const rows = [channel({ channel_no: 1 }), blank(2), channel({ channel_no: 3 })]
    const tail = collapseUnconfiguredTail(rows)
    expect(tail.collapsed).toBe(false)
    expect(tail.hidden).toHaveLength(0)
  })

  // One or two empty rows are not worth a toggle.
  test('leaves a short tail alone', () => {
    const rows = [channel({ channel_no: 1 }), blank(2)]
    expect(collapseUnconfiguredTail(rows).collapsed).toBe(false)
  })

  test('handles an all-empty list', () => {
    const tail = collapseUnconfiguredTail([blank(1), blank(2), blank(3)])
    expect(tail.before).toHaveLength(0)
    expect(tail.hidden).toHaveLength(3)
    expect(tail.collapsed).toBe(true)
  })
})

describe('channelRangeLabel', () => {
  test('names a run and a single channel', () => {
    expect(channelRangeLabel([{ channel_no: 9 }, { channel_no: 16 }])).toBe(
      'CH09–CH16'
    )
    expect(channelRangeLabel([{ channel_no: 9 }])).toBe('CH09')
    expect(channelRangeLabel([])).toBe('')
  })
})

describe('shortStamp', () => {
  const now = Date.parse('2026-10-18T12:00:00Z')

  // The column is narrow and every row was updated this year; repeating it costs
  // four characters and adds nothing.
  test('omits the current year', () => {
    const stamp = shortStamp(
      Date.parse('2026-10-18T00:19:00Z'),
      'Asia/Shanghai',
      now
    )
    expect(stamp).toBe('10-18 08:19')
    expect(stamp).not.toContain('2026')
  })

  // A timestamp from another year keeps it, because there the year is the
  // information.
  test('keeps a year that is not the current one', () => {
    const stamp = shortStamp(
      Date.parse('2025-10-18T00:19:00Z'),
      'Asia/Shanghai',
      now
    )
    expect(stamp).toBe('2025-10-18 08:19')
  })

  test('returns a dash for unusable input', () => {
    expect(shortStamp(Number.NaN, 'Asia/Shanghai')).toBe('—')
    expect(shortStamp(Date.now(), '')).toBe('—')
  })
})

describe('fullStamp', () => {
  test('always carries the full date for the title', () => {
    expect(fullStamp(Date.parse('2026-10-18T00:19:00Z'), 'Asia/Shanghai')).toBe(
      '2026-10-18 08:19'
    )
  })
})

describe('placeholderFor', () => {
  // "无" is a claim about a camera that does not exist yet; the rest of an
  // unconfigured row reads "—".
  test('uses a dash for an unconfigured channel', () => {
    expect(placeholderFor(blank(9), '')).toBe('—')
    expect(placeholderFor(blank(9), 'timeout')).toBe('—')
  })

  test('keeps 无 for a configured channel with no error', () => {
    expect(placeholderFor(channel(), '')).toBe('无')
    expect(placeholderFor(channel(), 'timeout')).toBe('timeout')
  })
})

describe('streamSummary', () => {
  // Several channels share one NVR address; the paths are what tell them apart.
  test('lists both paths so channels on one IP are distinguishable', () => {
    const text = streamSummary(channel())
    expect(text).toContain('192.168.66.110')
    expect(text).toContain('主: /Streaming/Channels/101')
    expect(text).toContain('子: /Streaming/Channels/102')
  })

  test('mentions the ONVIF port only when the revision has one', () => {
    expect(streamSummary(channel())).not.toContain('ONVIF')
    expect(streamSummary(channel({ onvif_port: 80 }))).toContain('ONVIF 端口: 80')
  })
})
