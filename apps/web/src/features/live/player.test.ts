import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiRequest } from '@/lib/api-client'
import { LivePlayer } from './player'

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
