import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiRequest } from '@/lib/api-client'
import { LivePlayer, type PlayerState } from './player'

vi.mock('@/lib/api-client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/api-client')>()),
  apiRequest: vi.fn(),
}))
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})
describe('live player cleanup', () => {
  it('releases a late lease when stopped during negotiation', async () => {
    let answer!: (value: unknown) => void
    const close = vi.fn()
    vi.stubGlobal(
      'RTCPeerConnection',
      class {
        iceGatheringState = 'complete'
        localDescription = { sdp: 'v=0' }
        addTransceiver() {}
        createOffer() {
          return Promise.resolve({ type: 'offer', sdp: 'v=0' })
        }
        setLocalDescription() {
          return Promise.resolve()
        }
        close = close
      }
    )
    vi.mocked(apiRequest).mockImplementation((path) =>
      path.endsWith('/live')
        ? new Promise((resolve) => {
            answer = resolve
          })
        : Promise.resolve({})
    )
    const player = new LivePlayer(
      document.createElement('video'),
      'channel',
      'sub',
      vi.fn()
    )
    const opening = player.start()
    await vi.waitFor(() => expect(answer).toBeTypeOf('function'))
    player.stop()
    answer({ id: 'late-peer', sdp: 'v=0', stream: 'sub', fallback: false })
    await opening
    expect(close).toHaveBeenCalled()
    expect(apiRequest).toHaveBeenCalledWith(
      '/api/v1/live-sessions/late-peer',
      expect.objectContaining({ method: 'DELETE' })
    )
  })
})
describe('live player track handling', () => {
  it('keeps one attached stream when a second track arrives late', async () => {
    // A camera whose stream carries sound makes ZLM answer the audio
    // transceiver as well, so ontrack fires twice. Reassigning srcObject for
    // the second track runs the media load algorithm, which aborts the play()
    // the video track started with AbortError: the tile then reported a
    // blocked browser for a camera that was streaming normally.
    const canvas = [document.createElement('canvas'), document.createElement('canvas')]
    const tracks = canvas.map(
      (element) => element.captureStream().getVideoTracks()[0]
    )
    vi.stubGlobal(
      'RTCPeerConnection',
      class {
        ontrack: ((event: { track: MediaStreamTrack }) => void) | null = null
        onconnectionstatechange: (() => void) | null = null
        iceGatheringState = 'complete'
        localDescription = { sdp: 'v=0' }
        addTransceiver() {}
        createOffer() {
          return Promise.resolve({ type: 'offer', sdp: 'v=0' })
        }
        setLocalDescription() {
          return Promise.resolve()
        }
        setRemoteDescription() {
          for (const track of tracks) this.ontrack?.({ track })
          return Promise.resolve()
        }
        close() {}
      }
    )
    vi.mocked(apiRequest).mockImplementation((path) =>
      path.endsWith('/live')
        ? Promise.resolve({
            id: 'lease',
            sdp: 'v=0',
            stream: 'sub',
            fallback: false,
          })
        : Promise.resolve({})
    )
    const video = document.createElement('video')
    let attachments = 0
    let attached: MediaStream | null = null
    Object.defineProperty(video, 'srcObject', {
      configurable: true,
      get: () => attached,
      set: (value: MediaStream | null) => {
        attachments++
        attached = value
      },
    })
    const play = vi.fn<() => Promise<void>>()
    play.mockRejectedValueOnce(
      new DOMException(
        'The play() request was interrupted by a new load request.',
        'AbortError'
      )
    )
    play.mockResolvedValue(undefined)
    video.play = play
    const states: PlayerState[] = []
    const player = new LivePlayer(video, 'channel', 'sub', (state) =>
      states.push(state)
    )
    await player.start()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(
      attachments,
      'srcObject must be attached once per attempt'
    ).toBe(1)
    expect(
      play,
      'playback must be requested once per attempt'
    ).toHaveBeenCalledTimes(1)
    expect(
      states.filter((state) => state.phase === 'error'),
      'a superseded play() must not be reported as a blocked browser'
    ).toHaveLength(0)
    expect((attached as MediaStream | null)?.getTracks()).toHaveLength(2)
    expect(
      tracks.every((track) => track.readyState !== 'ended'),
      'both tracks must still be live while the tile is open'
    ).toBe(true)
    player.stop()
  })
})
