import type { Schema } from '@/lib/types'

// ZLM's WebRTC endpoint only packetizes opus, PCMA and PCMU into RTP; any
// other camera audio codec (typically AAC) is rejected at signalling time and
// the live preview plays that stream without sound. The list marks those rows
// instead of letting the operator discover it inside the player.
const PREVIEWABLE_AUDIO = new Set(['pcm_alaw', 'pcm_mulaw', 'opus'])

const VIDEO_CODEC_NAMES: Record<string, string> = {
  h264: 'H.264',
  hevc: 'H.265',
}

const AUDIO_CODEC_NAMES: Record<string, string> = {
  pcm_alaw: 'PCMA',
  pcm_mulaw: 'PCMU',
  opus: 'Opus',
  aac: 'AAC',
}

// "2560×1440 H.264 25fps" — only the parameters the test actually observed.
export function videoParamsLabel(media: Schema<'ChannelMediaParams'>): string {
  const parts: string[] = []
  if (media.width && media.height) parts.push(`${media.width}×${media.height}`)
  if (media.codec)
    parts.push(VIDEO_CODEC_NAMES[media.codec] ?? media.codec.toUpperCase())
  if (media.fps && media.fps > 0)
    parts.push(`${parseFloat(media.fps.toFixed(2))}fps`)
  return parts.join(' ')
}

// Three distinct meanings: not captured yet (null), the stream genuinely has
// no audio (""), or the codec name reported by the probe.
export function audioCodecLabel(codec: string | null | undefined): string {
  if (codec == null) return '—'
  if (codec === '') return '无音频'
  return AUDIO_CODEC_NAMES[codec] ?? codec
}

export function audioPreviewable(codec: string | null | undefined): boolean {
  return codec != null && codec !== '' && PREVIEWABLE_AUDIO.has(codec)
}
