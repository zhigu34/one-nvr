import { contentPath } from './api'
import {
  entryAt,
  gapAt,
  nextPlayableEntry,
  offsetIn,
  type Entry,
  type Gap,
  type Timeline,
} from './timeline'

/**
 * The subset of a media element the controller drives. Keeping it structural
 * lets the playback rules be tested without real media, while the page passes
 * the browser's own element through.
 */
export type MediaElement = {
  src: string
  currentTime: number
  playbackRate: number
  error?: { code?: number } | null
  play(): Promise<void>
  pause(): void
  load(): void
  addEventListener(type: string, listener: () => void): void
  removeEventListener(type: string, listener: () => void): void
}

export type Phase = 'idle' | 'loading' | 'playing' | 'paused' | 'gap' | 'error'

export type PlaybackState = {
  phase: Phase
  /** Absolute position on the site clock, in epoch milliseconds. */
  clock: number
  message: string
  entry: Entry | null
  /** Set while the requested position has no playable segment. */
  gap: Gap | null
  /** A gap playback crossed on its own; the user must be told about it. */
  skipped: Gap | null
  rate: number
}

export const RATES = [0.5, 1, 2, 4] as const

const EMPTY: Timeline = { entries: [], gaps: [], from: 0, to: 0 }

function errorMessage(code: number | undefined) {
  if (code === 2) return '读取录像失败，请确认存储池是否在线'
  if (code === 3) return '该片段解码失败，文件可能已损坏'
  if (code === 4) return '该片段无法播放，文件可能已被移走或尚未完成发布'
  return '播放中断，请重试'
}

/**
 * Drives one video element across a timeline of segments. The browser owns the
 * byte ranges: assigning `src` to the authorized content endpoint makes it
 * issue same-origin GET requests carrying the session cookie, and its own
 * seek/abort behaviour is exactly what the Range endpoint is built for.
 */
export class PlaybackController {
  private timeline: Timeline = EMPTY
  private entry: Entry | null = null
  private rate = 1
  private playing = false
  private stopped = true
  private generation = 0
  private pendingOffset = 0
  private skipped: Gap | null = null
  private state: PlaybackState = {
    phase: 'idle',
    clock: 0,
    message: '选择通道与时间后开始播放',
    entry: null,
    gap: null,
    skipped: null,
    rate: 1,
  }
  private listeners: [string, () => void][] = []

  constructor(
    private media: MediaElement,
    /** Explains a failed segment through the API; empty when it is readable. */
    private probe: (entry: Entry) => Promise<string>,
    private update: (state: PlaybackState) => void
  ) {
    this.bind('loadedmetadata', () => this.ready())
    this.bind('timeupdate', () => this.tick())
    this.bind('playing', () => {
      this.playing = true
      this.publish('playing', '播放中')
    })
    this.bind('pause', () => {
      this.playing = false
      if (!this.stopped && this.entry) this.publish('paused', '已暂停')
    })
    this.bind('ended', () => this.advance())
    this.bind('error', () => void this.fail())
  }

  setTimeline(timeline: Timeline) {
    this.timeline = timeline
  }

  get current() {
    return this.state
  }

  /** Starts playback at an absolute position, or reports why it cannot. */
  start(at: number) {
    this.stopped = false
    this.skipped = null
    this.seekTo(at)
  }

  seek(at: number) {
    if (this.stopped) return this.start(at)
    this.skipped = null
    this.seekTo(at)
  }

  /** Moves by seconds, crossing into the neighbouring segment when needed. */
  shift(seconds: number) {
    this.seek(this.state.clock + seconds * 1000)
  }

  pause() {
    this.playing = false
    this.media.pause()
    if (this.entry) this.publish('paused', '已暂停')
  }

  resume() {
    if (!this.entry) return this.start(this.state.clock)
    this.stopped = false
    void this.play()
  }

  setRate(rate: number) {
    if (!RATES.includes(rate as (typeof RATES)[number])) return
    this.rate = rate
    this.media.playbackRate = rate
    this.state = { ...this.state, rate }
    this.update(this.state)
  }

  /** Jumps forward to the next playable segment, as offered after a gap. */
  jumpToNext() {
    this.skipped = null
    const next = nextPlayableEntry(this.timeline, this.state.clock)
    if (!next) {
      this.entry = null
      this.media.pause()
      this.emit({
        phase: 'gap',
        clock: this.state.clock,
        message: '该时间范围之后没有已完成的录像',
        entry: null,
        gap: null,
      })
      return
    }
    this.load(next, 0)
  }

  stop() {
    this.stopped = true
    this.entry = null
    this.playing = false
    this.skipped = null
    ++this.generation
    this.media.pause()
    this.media.src = ''
    this.emit({
      phase: 'idle',
      clock: this.state.clock,
      message: '已停止播放',
      entry: null,
    })
  }

  dispose() {
    this.stopped = true
    this.playing = false
    ++this.generation
    this.media.pause()
    this.media.src = ''
    for (const [type, listener] of this.listeners)
      this.media.removeEventListener(type, listener)
    this.listeners = []
  }

  private bind(type: string, listener: () => void) {
    this.media.addEventListener(type, listener)
    this.listeners.push([type, listener])
  }

  private seekTo(at: number) {
    const entry = entryAt(this.timeline, at)
    if (!entry) {
      const gap = gapAt(this.timeline, at)
      this.entry = null
      this.playing = false
      ++this.generation
      this.media.pause()
      this.emit({
        phase: 'gap',
        clock: at,
        message: gap?.reason || '该时间点没有已完成的录像',
        entry: null,
        gap,
      })
      return
    }
    const offset = offsetIn(entry, at)
    if (
      entry.id === this.entry?.id &&
      this.media.src === contentPath(entry.id)
    ) {
      // Same segment: seeking in place makes the browser issue one ranged
      // request instead of re-reading the segment header.
      this.media.currentTime = offset
      this.emit({
        phase: this.playing ? 'playing' : 'paused',
        clock: at,
        message: this.playing ? '播放中' : '已暂停',
        entry,
        gap: null,
      })
      return
    }
    this.load(entry, offset)
  }

  private load(entry: Entry, offset: number) {
    ++this.generation
    this.entry = entry
    this.playing = false
    this.pendingOffset = offset
    this.media.src = contentPath(entry.id)
    this.media.playbackRate = this.rate
    this.media.load()
    this.emit({
      phase: 'loading',
      clock: entry.start + offset * 1000,
      message: '正在打开片段…',
      entry,
      gap: null,
    })
  }

  private ready() {
    if (!this.entry || this.stopped) return
    // Assigning zero would seek a stream that already starts at zero.
    if (this.pendingOffset > 0) this.media.currentTime = this.pendingOffset
    this.pendingOffset = 0
    void this.play()
  }

  private async play() {
    // A late play() resolution must not resurrect playback after a failure or
    // a segment change, so the state is only published if nothing moved on.
    const token = this.generation
    try {
      await this.media.play()
      if (token !== this.generation) return
      // play() resolving means playback started; the element is not required to
      // emit `playing` for every resumed seek, so the state is set here too.
      this.playing = true
      this.publish('playing', '播放中')
    } catch {
      if (token !== this.generation) return
      this.playing = false
      this.emit({
        phase: 'paused',
        clock: this.state.clock,
        message: '浏览器阻止了自动播放，请点击播放',
        entry: this.entry,
      })
    }
  }

  private tick() {
    if (!this.entry || this.stopped) return
    this.publish('playing', '播放中')
  }

  private publish(phase: Phase, message: string) {
    this.emit({
      phase,
      clock: this.entry
        ? this.entry.start + this.media.currentTime * 1000
        : this.state.clock,
      message,
      entry: this.entry,
      gap: null,
    })
  }

  /**
   * PLAY-03: a hole is never crossed silently. Playback continues into the next
   * segment, but the skipped interval travels with the state until the user
   * seeks again, so the page can show exactly what was skipped and why.
   */
  private advance() {
    const current = this.entry
    if (!current || this.stopped) return
    const next = nextPlayableEntry(this.timeline, current.end)
    if (!next) {
      this.entry = null
      this.playing = false
      ++this.generation
      this.emit({
        phase: 'gap',
        clock: current.end,
        message: '已播放到该时间范围最后一个可用片段',
        entry: null,
        gap: gapAt(this.timeline, current.end),
      })
      return
    }
    this.skipped =
      next.start > current.end
        ? {
            start: current.end,
            end: next.start,
            reason: '该时间段没有已完成的录像',
          }
        : null
    this.load(next, 0)
  }

  /**
   * The media element cannot report an HTTP status, so its own error is shown
   * immediately and then refined by asking the API why the segment is
   * unavailable. The probe never runs on the success path, so a healthy
   * playback costs exactly one media request per segment.
   */
  private async fail() {
    if (this.stopped) return
    const entry = this.entry
    const token = ++this.generation
    const clock = this.state.clock
    this.entry = null
    this.playing = false
    this.emit({
      phase: 'error',
      clock,
      message: errorMessage(this.media.error?.code),
      entry: null,
      gap: null,
    })
    if (!entry) return
    const problem = await this.probe(entry)
    if (token !== this.generation || !problem) return
    this.emit({
      phase: 'error',
      clock,
      message: problem,
      entry: null,
      gap: null,
    })
  }

  private emit(
    partial: Partial<PlaybackState> &
      Pick<PlaybackState, 'phase' | 'clock' | 'message' | 'entry'>
  ) {
    this.state = {
      phase: partial.phase,
      clock: partial.clock,
      message: partial.message,
      entry: partial.entry,
      gap: partial.gap ?? null,
      skipped: partial.skipped ?? this.skipped,
      rate: partial.rate ?? this.rate,
    }
    this.update(this.state)
  }
}
