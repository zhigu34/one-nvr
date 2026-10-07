import { describe, expect, it } from 'vitest'
import { restoreView } from './view'

describe('live view persistence', () => {
  it('keeps only permitted channels and supported layouts', () => {
    expect(
      restoreView(
        '{"layout":4,"channels":["allowed","revoked","allowed"],"sdp":"private"}',
        ['allowed']
      )
    ).toEqual({ layout: 4, channels: ['allowed'] })
    expect(
      restoreView('{"layout":32,"channels":["allowed"]}', ['allowed'])
    ).toEqual({ layout: 4, channels: ['allowed'] })
  })
  it('ignores malformed persisted data', () => {
    expect(restoreView('garbage', ['a'])).toEqual({ layout: 4, channels: [] })
  })
})
