import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Schema } from '@/lib/types'
import {
  PlaybackController,
  type MediaElement,
  type PlaybackState,
} from './player'
import { buildTimeline } from './timeline'

const base = Date.parse('2026-10-05T00:00:00Z')
const at = (seconds: number) => new Date(base + seconds * 1000).toISOString()
function segment(
  id: string,
  from: number,
  to: number,
  overrides: Partial<Schema<'RecordingSegment'>> = {}
): Schema<'RecordingSegment'> {
  return {
    id,
    channel_id: 'channel',
    source_revision_id: 'revision',
    pool_id: 'pool',
    run_id: 'run',
    start: at(from),
    end: at(to),
    bytes: 1024,
    state: 'ready',
  protected: false,
  media: { codec: '', width: 0, height: 0, fps: 0, duration_ms: 0 },
    naming_timezone: 'Asia/Shanghai',
    utc_offset_seconds: 28800,
    check_state: 'ready',
    ...overrides,
  }
}

class FakeMedia implements MediaElement {
  src = ''
  currentTime = 0
  // Real elements report the resource duration; the controller reads it to
  // decide when the next segment is close enough to prefetch.
  duration = 18
  playbackRate = 1
  error: { code?: number } | null = null
  plays = 0
  pauses = 0
  loads = 0
  private handlers = new Map<string, Set<() => void>>()
  addEventListener(type: string, listener: () => void) {
    const bucket = this.handlers.get(type) || new Set<() => void>()
    bucket.add(listener)
    this.handlers.set(type, bucket)
  }
  removeEventListener(type: string, listener: () => void) {
    this.handlers.get(type)?.delete(listener)
  }
  listenerCount() {
    return [...this.handlers.values()].reduce(
      (total, set) => total + set.size,
      0
    )
  }
  play() {
    this.plays++
    return Promise.resolve()
  }
  pause() {
    this.pauses++
  }
  load() {
    this.loads++
    // The spec's resource selection resets playbackRate to defaultPlaybackRate
    // (1) whenever a new resource loads; modelling that here is what pins the
    // re-apply in ready().
    this.playbackRate = 1
  }
  emit(type: string) {
    for (const listener of [...(this.handlers.get(type) || [])]) listener()
  }
}

function fixture(
  segments: Schema<'RecordingSegment'>[],
  probe = vi.fn(async () => '')
) {
  const media = new FakeMedia()
  const seen: PlaybackState[] = []
  const controller = new PlaybackController(media, probe, (state) =>
    seen.push(state)
  )
  controller.setTimeline(buildTimeline(segments, base, base + 300_000))
  return {
    media,
    probe,
    controller,
    seen,
    state: () => seen[seen.length - 1],
  }
}

afterEach(() => vi.restoreAllMocks())

describe('recording playback controller', () => {
  it('opens the segment covering the requested position and seeks inside it', () => {
    const f = fixture([segment('a', 0, 60), segment('b', 120, 180)])
    f.controller.start(base + 30_000)
    expect(f.media.src).toBe('/api/v1/recordings/a/content')
    expect(f.media.loads).toBe(1)
    expect(f.state().phase).toBe('loading')
    f.media.emit('loadedmetadata')
    expect(f.media.currentTime).toBe(30)
    expect(f.media.plays).toBe(1)
    f.media.currentTime = 31
    f.media.emit('timeupdate')
    expect(f.state().phase).toBe('playing')
    expect(f.state().clock).toBe(base + 31_000)
  })

  it('reports a hole instead of starting media playback', () => {
    const f = fixture([segment('a', 0, 60), segment('b', 120, 180)])
    f.controller.start(base + 90_000)
    expect(f.media.src).toBe('')
    expect(f.media.loads).toBe(0)
    expect(f.state().phase).toBe('gap')
    expect(f.state().gap).toEqual({
      start: base + 60_000,
      end: base + 120_000,
      reason: '该时间段没有已完成的录像',
    })
  })

  it('crosses a hole only while telling the user what was skipped', () => {
    const f = fixture([segment('a', 0, 60), segment('b', 120, 180)])
    f.controller.start(base)
    f.media.emit('loadedmetadata')
    f.media.emit('ended')
    expect(f.media.src).toBe('/api/v1/recordings/b/content')
    expect(f.state().skipped).toEqual({
      start: base + 60_000,
      end: base + 120_000,
      reason: '该时间段没有已完成的录像',
    })
    // A contiguous neighbour is not a hole and must stay unnoticed.
    const contiguous = fixture([segment('a', 0, 60), segment('c', 60, 120)])
    contiguous.controller.start(base)
    contiguous.media.emit('loadedmetadata')
    contiguous.media.emit('ended')
    expect(contiguous.state().skipped).toBeNull()
  })

  it('stops at the end of the index instead of pretending playback continues', () => {
    const f = fixture([segment('a', 0, 60)])
    f.controller.start(base)
    f.media.emit('loadedmetadata')
    f.media.emit('ended')
    expect(f.state().phase).toBe('gap')
    expect(f.state().message).toContain('最后一个可用片段')
  })

  it('shifts by seconds, seeking in place and crossing segment boundaries', () => {
    const f = fixture([segment('a', 0, 60), segment('c', 60, 120)])
    f.controller.start(base + 30_000)
    f.media.emit('loadedmetadata')
    f.media.currentTime = 30
    f.media.emit('timeupdate')
    f.controller.shift(-10)
    // Same segment: the media element is seeked instead of being reloaded.
    expect(f.media.currentTime).toBe(20)
    expect(f.media.src).toBe('/api/v1/recordings/a/content')
    f.media.currentTime = 55
    f.media.emit('timeupdate')
    f.controller.shift(10)
    expect(f.media.src).toBe('/api/v1/recordings/c/content')
    f.media.emit('loadedmetadata')
    expect(f.media.currentTime).toBe(5)
  })

  it('reports a hole when a ten second step lands inside one', () => {
    const f = fixture([segment('a', 0, 60), segment('b', 120, 180)])
    f.controller.start(base + 55_000)
    f.media.emit('loadedmetadata')
    f.media.currentTime = 55
    f.media.emit('timeupdate')
    f.controller.shift(10)
    expect(f.state().phase).toBe('gap')
    expect(f.state().clock).toBe(base + 65_000)
  })

  it('jumps to the next playable segment after a hole', () => {
    const f = fixture([segment('a', 0, 60), segment('b', 120, 180)])
    f.controller.start(base + 90_000)
    expect(f.state().phase).toBe('gap')
    f.controller.jumpToNext()
    expect(f.media.src).toBe('/api/v1/recordings/b/content')
    expect(f.state().phase).toBe('loading')
  })

  it('accepts only the documented rates', () => {
    const f = fixture([segment('a', 0, 60)])
    f.controller.setRate(4)
    expect(f.media.playbackRate).toBe(4)
    expect(f.state().rate).toBe(4)
    f.controller.setRate(3)
    expect(f.media.playbackRate).toBe(4)
  })

  it('keeps the chosen rate when playback crosses into the next segment', () => {
    const f = fixture([segment('a', 0, 60), segment('b', 120, 180)])
    f.controller.start(base + 30_000)
    f.controller.setRate(4)
    f.media.emit('loadedmetadata')
    f.media.emit('ended')
    // The reload answered with the spec reset the fake models…
    expect(f.media.playbackRate).toBe(1)
    // …and the controller must answer that reset, not leave 1x playing at 4x.
    f.media.emit('loadedmetadata')
    expect(f.media.playbackRate).toBe(4)
    expect(f.state().rate).toBe(4)
  })

  it('refines a media failure with the reason the API reports', async () => {
    const probe = vi.fn(async () => '资源不存在')
    const f = fixture([segment('a', 0, 60)], probe)
    f.controller.start(base)
    f.media.emit('loadedmetadata')
    f.media.error = { code: 4 }
    f.media.emit('error')
    expect(f.state().phase).toBe('error')
    expect(f.state().message).toContain('无法播放')
    await vi.waitFor(() => expect(f.state().message).toBe('资源不存在'))
    expect(probe).toHaveBeenCalledTimes(1)
  })

  it('keeps the local diagnosis when the segment is still readable', async () => {
    const f = fixture(
      [segment('a', 0, 60)],
      vi.fn(async () => '')
    )
    f.controller.start(base)
    f.media.emit('loadedmetadata')
    f.media.error = { code: 3 }
    f.media.emit('error')
    await Promise.resolve()
    expect(f.state().message).toContain('解码失败')
  })

  it('detaches every listener and stops the element on dispose', () => {
    const f = fixture([segment('a', 0, 60)])
    f.controller.start(base)
    expect(f.media.listenerCount()).toBeGreaterThan(0)
    f.controller.dispose()
    expect(f.media.listenerCount()).toBe(0)
    expect(f.media.src).toBe('')
  })
})

// Advancing only on `ended` makes the element silent, then ask for the next
// file and wait for its headers and first frames — a visible hitch at every
// segment boundary on an 18-second cut. The next segment's opening bytes are
// fetched while the current one is still playing.
describe('prefetching the next segment', () => {
  const base = Date.parse('2026-10-05T00:00:00Z')

  it('starts fetching the next segment shortly before this one ends', async () => {
    const fetchSpy = vi.fn(async () => new Response(new Uint8Array(16), { status: 206 }))
    const original = globalThis.fetch
    globalThis.fetch = fetchSpy as unknown as typeof fetch
    try {
      const f = fixture([segment('seg-a', 0, 18), segment('seg-b', 18, 36)])
      f.controller.start(base)
      f.media.emit('loadedmetadata')
      // The prefetch only runs while playing (a paused element has no "about to
      // finish" to act on).
      f.media.emit('playing')
      // Far from the end: nothing to prefetch yet.
      f.media.currentTime = 4
      f.media.emit('timeupdate')
      expect(fetchSpy).not.toHaveBeenCalled()

      // Inside the lead window: the next segment is fetched once.
      f.media.currentTime = 15
      f.media.emit('timeupdate')
      f.media.emit('timeupdate')
      expect(fetchSpy).toHaveBeenCalledTimes(1)
      const [url, init] = fetchSpy.mock.calls[0] as unknown as [string, RequestInit]
      expect(url).toContain('seg-b')
      // A ranged request: buffering the whole next file would waste bandwidth
      // when the operator stops here.
      expect((init.headers as Record<string, string>).Range).toBe('bytes=0-65535')
    } finally {
      globalThis.fetch = original
    }
  })
})
