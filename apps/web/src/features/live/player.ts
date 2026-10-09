import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'

export type PlayerState = {
  phase: 'connecting' | 'playing' | 'error' | 'retrying'
  message: string
  stream?: string
  fallback?: boolean
}
type Answer = { id: string; sdp: string; stream: string; fallback: boolean }

export class LivePlayer {
  private peer: RTCPeerConnection | null = null
  private lease = ''
  private stopped = false
  private generation = 0
  private retries = 0
  private heartbeat?: ReturnType<typeof setInterval>
  private watchdog?: ReturnType<typeof setInterval>
  private timeout?: ReturnType<typeof setTimeout>
  private retry?: ReturnType<typeof setTimeout>
  private frame?: number
  private lastFrame = 0
  private reported = false
  private answer?: Answer
  private cancelGather?: () => void
  // The element is given exactly one MediaStream per attempt. Reassigning
  // srcObject runs the media load algorithm, and an answer that carries an
  // audio track fires ontrack twice; reassigning there would abort the play()
  // started by the video track with AbortError.
  private mediaStream: MediaStream | null = null
  private playRequested = false
  constructor(
    private video: HTMLVideoElement,
    private channel: string,
    private stream: 'main' | 'sub',
    private update: (state: PlayerState) => void
  ) {}

  async start() {
    if (this.stopped) return
    const attempt = ++this.generation
    this.reported = false
    this.lastFrame = 0
    this.update({ phase: 'connecting', message: '正在连接…' })
    try {
      if (!window.RTCPeerConnection)
        throw new ApiError(
          422,
          'rtc_unavailable',
          '浏览器不支持 WebRTC，请使用新版 Chrome、Edge 或 Firefox'
        )
      const peer = new RTCPeerConnection({ iceServers: [] })
      this.peer = peer
      // Attach the owned stream before the answer arrives so the first track
      // only extends it. Later tracks are added to the same stream, which
      // never runs the load algorithm and never interrupts playback.
      this.mediaStream = new MediaStream()
      this.playRequested = false
      this.video.srcObject = this.mediaStream
      peer.addTransceiver('video', { direction: 'recvonly' })
      peer.addTransceiver('audio', { direction: 'recvonly' })
      peer.ontrack = (event) => {
        if (attempt !== this.generation || !this.mediaStream) return
        this.mediaStream.addTrack(event.track)
        if (this.playRequested) return
        this.playRequested = true
        void this.video.play().catch((error: unknown) => {
          // Only a policy refusal is the operator's problem to fix. An
          // AbortError means this request was superseded, which is not a
          // reason to report a blocked browser.
          if (error instanceof DOMException && error.name === 'AbortError')
            return
          this.fail(
            new ApiError(
              422,
              'autoplay_blocked',
              '浏览器阻止播放，请保持静音后重新连接'
            ),
            attempt
          )
        })
      }
      peer.onconnectionstatechange = () => {
        if (['failed', 'disconnected', 'closed'].includes(peer.connectionState))
          this.fail(new Error('视频连接已中断'), attempt)
      }
      await peer.setLocalDescription(await peer.createOffer())
      if (attempt !== this.generation) return
      await this.gather(peer)
      if (attempt !== this.generation) return
      const answer = await apiRequest<Answer>(
        `/api/v1/channels/${this.channel}/live`,
        jsonRequest('POST', {
          stream: this.stream,
          sdp: peer.localDescription?.sdp,
        })
      )
      if (attempt !== this.generation) {
        this.release(answer.id)
        return
      }
      this.lease = answer.id
      this.answer = answer
      await peer.setRemoteDescription({ type: 'answer', sdp: answer.sdp })
      if (attempt !== this.generation) return
      this.heartbeat = setInterval(() => {
        void apiRequest(
          `/api/v1/live-sessions/${answer.id}/renew`,
          jsonRequest('POST')
        ).catch((error: unknown) => this.fail(error, attempt))
      }, 10000)
      this.timeout = setTimeout(
        () =>
          this.fail(
            new Error('未收到视频画面，请检查 RTC 端口、防火墙和媒体地址'),
            attempt
          ),
        12000
      )
      const frame = () => {
        if (attempt !== this.generation) return
        this.markFrame()
        this.frame = this.video.requestVideoFrameCallback(frame)
      }
      if (this.video.requestVideoFrameCallback)
        this.frame = this.video.requestVideoFrameCallback(frame)
      this.watchdog = setInterval(() => {
        if (
          !this.video.requestVideoFrameCallback &&
          this.video.getVideoPlaybackQuality().totalVideoFrames > 0 &&
          !this.video.paused
        )
          this.markFrame()
        if (this.lastFrame && Date.now() - this.lastFrame > 8000)
          this.fail(new Error('视频画面停止更新'), attempt)
      }, 1000)
    } catch (error) {
      this.fail(error, attempt)
    }
  }
  private markFrame() {
    this.lastFrame = Date.now()
    if (!this.reported) {
      this.reported = true
      clearTimeout(this.timeout)
      this.update({
        phase: 'playing',
        message: '播放中',
        stream: this.answer?.stream,
        fallback: this.answer?.fallback,
      })
    }
  }
  private gather(peer: RTCPeerConnection): Promise<void> {
    if (peer.iceGatheringState === 'complete') return Promise.resolve()
    return new Promise((resolve, reject) => {
      const done = (error?: Error) => {
        clearTimeout(timer)
        peer.removeEventListener('icegatheringstatechange', changed)
        this.cancelGather = undefined
        if (error) reject(error)
        else resolve()
      }
      const changed = () => {
        if (peer.iceGatheringState === 'complete') done()
      }
      const timer = setTimeout(
        () => done(new Error('浏览器网络协商超时')),
        5000
      )
      this.cancelGather = () => done(new Error('预览已关闭'))
      peer.addEventListener('icegatheringstatechange', changed)
    })
  }
  private release(lease: string) {
    void apiRequest(`/api/v1/live-sessions/${lease}`, {
      ...jsonRequest('DELETE'),
      keepalive: true,
    }).catch(() => {})
  }
  private cleanup() {
    this.cancelGather?.()
    clearInterval(this.heartbeat)
    clearInterval(this.watchdog)
    clearTimeout(this.timeout)
    if (this.frame !== undefined)
      this.video.cancelVideoFrameCallback(this.frame)
    if (this.peer) {
      this.peer.ontrack = null
      this.peer.onconnectionstatechange = null
      this.peer.close()
      this.peer = null
    }
    if (this.video.srcObject instanceof MediaStream)
      this.video.srcObject.getTracks().forEach((track) => track.stop())
    this.video.srcObject = null
    this.mediaStream = null
    this.playRequested = false
    if (this.lease) {
      this.release(this.lease)
      this.lease = ''
    }
  }
  private fail(error: unknown, attempt: number) {
    if (attempt !== this.generation || this.stopped) return
    ++this.generation
    this.cleanup()
    const message = error instanceof Error ? error.message : '无法播放此通道'
    const terminal =
      error instanceof ApiError &&
      (error.status === 401 ||
        error.status === 403 ||
        error.status === 404 ||
        error.status === 429 ||
        [
          'live_codec_unsupported',
          'rtc_unavailable',
          'autoplay_blocked',
        ].includes(error.code))
    if (!terminal && this.retries < 3) {
      const delay = 1000 * 2 ** this.retries++
      this.update({
        phase: 'retrying',
        message: `${message}，正在重连 (${this.retries}/3)`,
      })
      this.retry = setTimeout(() => void this.start(), delay)
    } else this.update({ phase: 'error', message })
  }
  stop() {
    this.stopped = true
    ++this.generation
    clearTimeout(this.retry)
    this.cleanup()
  }
}
