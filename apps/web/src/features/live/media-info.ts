// Live tiles report what the browser is actually decoding right now, not what
// the camera advertises. ZLM's WebRTC endpoint only packetizes the camera
// stream (no transcode anywhere in the live pipeline), so the mode is always
// 直通 and the label says so instead of leaving the operator guessing.
export type LiveMediaInfo = {
  width: number
  height: number
  fps: number
  videoCodec: string
  audioCodec: string | null
}

export function liveMediaLabel(info: LiveMediaInfo): string {
  const parts: string[] = []
  if (info.width > 0 && info.height > 0)
    parts.push(`${info.width}×${info.height}`)
  if (info.fps > 0) parts.push(`${Math.round(info.fps)}fps`)
  parts.push('直通')
  parts.push(info.audioCodec ? `音 ${info.audioCodec}` : '无音频')
  return parts.join(' · ')
}

export function liveMediaTitle(info: LiveMediaInfo): string {
  const video = info.videoCodec ? `（${info.videoCodec}）` : ''
  const audio = info.audioCodec
    ? `音频编码 ${info.audioCodec}`
    : '无音频：摄像头未提供音频，或音频编码不被浏览器 WebRTC 支持（如 AAC）'
  return `直通（未转码）：浏览器直接解码摄像头原始编码${video}；${audio}`
}
