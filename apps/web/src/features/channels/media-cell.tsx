import type { Schema } from '@/lib/types'
import { formatMinute } from '@/features/playback/zone'
import { audioCodecLabel, audioPreviewable } from './media'

type Media = Schema<'ChannelMediaParams'>

/**
 * Megapixel shorthand: 2560×1920 → "4MP". Ties round down the way camera
 * vendors label them; unknown sizes keep the full resolution instead of an
 * invented round figure.
 */
function megapixel(width: number | undefined, height: number | undefined) {
  if (!width || !height) return ''
  const mp = (width * height) / 1_000_000
  if (mp < 0.5) return ''
  const steps = [0.5, 1, 2, 3, 4, 5, 6, 8, 12]
  let picked: number = steps[0]
  for (const step of steps) if (mp >= step) picked = step
  return `${picked}MP`
}

const VIDEO_CODEC_NAMES: Record<string, string> = {
  h264: 'H.264',
  hevc: 'H.265',
}

function codecLabel(codec: string | undefined) {
  return codec ? (VIDEO_CODEC_NAMES[codec] ?? codec.toUpperCase()) : ''
}

function fpsLabel(media: Media) {
  return media.fps && media.fps > 0
    ? `${parseFloat(media.fps.toFixed(2))}fps`
    : ''
}

/** Bit rate in the unit an operator reads at a glance: kbps below 1 Mbps. */
function bitrateLabel(kbps: number | undefined) {
  if (!kbps || kbps <= 0) return ''
  return kbps >= 1000 ? `${(kbps / 1000).toFixed(1)}M` : `${kbps}k`
}

const AUDIO_CHANNEL_NAMES: Record<number, string> = {
  1: '单声道',
  2: '立体声',
}

function audioDetail(media: Media) {
  const parts: string[] = []
  if (media.audio_sample_rate) parts.push(`${media.audio_sample_rate / 1000}kHz`)
  const channels = media.audio_channels
  if (channels) parts.push(AUDIO_CHANNEL_NAMES[channels] ?? `${channels}ch`)
  const bitrate = bitrateLabel(media.audio_bitrate_kbps)
  if (bitrate) parts.push(bitrate)
  return parts.join(' ')
}

/**
 * The audio pill: only rendered when the probe actually captured a codec.
 * "No audio" stays silent — printing it on every row was pure noise.
 *
 * The pill carries the codec only; the sample rate, channel layout and bit rate
 * live in the row's hover title, because a 640×480 mono 8 kHz track is not
 * something an operator needs in a table cell but is exactly what they need when
 * asking why the picture and sound do not match.
 */
function AudioPill({ media }: { media: Media | null | undefined }) {
  const codec = media?.audio_codec
  if (codec == null || codec === '') return null
  const label = audioCodecLabel(codec)
  const detail = audioDetail(media ?? ({} as Media))
  const title = detail
    ? `${label} · ${detail}${audioPreviewable(codec) ? ' · 实时预览可出声' : ' · 实时预览无声（WebRTC 只转发 opus/PCMA/PCMU）'}`
    : audioPreviewable(codec)
      ? '实时预览可出声'
      : '实时预览无声：ZLM 的 WebRTC 只支持 opus / PCMA / PCMU，其余编码（如 AAC）不会被转发'
  if (audioPreviewable(codec))
    return (
      <span
        title={title}
        className='rounded-full border border-emerald-700/40 bg-emerald-950/40 px-1.5 py-px text-[10px] leading-4 text-emerald-400'
      >
        {label}
      </span>
    )
  return (
    <span
      title={title}
      className='rounded-full border border-amber-700/40 bg-amber-950/30 px-1.5 py-px text-[10px] leading-4 text-amber-400'
    >
      {label}
    </span>
  )
}

/** One line per stream: resolution, codec, profile, fps, bit rate. */
function streamLine(media: Media, withMegapixels: boolean) {
  const resolution =
    media.width && media.height ? `${media.width}×${media.height}` : ''
  const size = withMegapixels ? megapixel(media.width, media.height) : ''
  return [
    // The shorthand leads, and the exact pixel count follows it: "4MP" alone
    // hides that a 4MP sensor is actually streaming 704×576.
    size,
    resolution,
    codecLabel(media.codec),
    media.profile ?? '',
    fpsLabel(media),
    bitrateLabel(media.bitrate_kbps),
  ].filter(Boolean)
}

/** The full technical picture, shown on hover. Absent facts are omitted. */
function detailTitle(main: Media | null | undefined, sub: Media | null | undefined, observed: string | null | undefined, zone: string) {
  const rows: string[] = []
  const describe = (name: string, media: Media | null | undefined) => {
    if (!media) return
    const parts: string[] = []
    if (media.width && media.height) parts.push(`${media.width}×${media.height}`)
    if (media.sample_aspect && media.sample_aspect !== '1:1') {
      // Anamorphic video stores fewer pixels than it displays; saying so stops
      // "704×576" from being read as a 4:3 picture when it renders as 16:9.
      parts.push(`SAR ${media.sample_aspect}`)
    }
    if (media.codec) {
      const profile = media.profile ? ` ${media.profile}` : ''
      const level = media.level ? ` L${media.level}` : ''
      parts.push(`${codecLabel(media.codec)}${profile}${level}`)
    }
    if (media.pixel_format) parts.push(media.pixel_format)
    if (media.fps) parts.push(`${parseFloat(media.fps.toFixed(2))}fps`)
    if (media.bitrate_kbps) parts.push(`${media.bitrate_kbps} kbps`)
    if (media.audio_codec) {
      const audio = [audioCodecLabel(media.audio_codec)]
      const detail = audioDetail(media)
      if (detail) audio.push(detail)
      parts.push(audio.join(' '))
    }
    if (parts.length) rows.push(`${name}：${parts.join(' · ')}`)
  }
  describe('主流', main)
  describe('子流', sub)
  if (observed && zone) rows.push(`最近观测：${formatMinute(Date.parse(observed), zone)}`)
  return rows.join('\n')
}

/**
 * Media parameters are the applied revision's newest successful test result.
 * They are reference information, so they keep showing while the camera is
 * unreachable, and the hover title tells the operator how old they are.
 *
 * Layout: the main stream leads with its megapixel shorthand, the sub stream
 * beneath it in a dimmer line. Both carry resolution, codec, profile, frame rate
 * and declared bit rate — a row that showed only "4MP · H.265 · 20fps" hid the
 * two facts most often responsible for a soft or unplayable picture (the actual
 * pixel count and the bit rate the encoder was given).
 */
export function MediaParamsCell({
  channel,
  zone,
}: {
  channel: Schema<'ChannelSummary'>
  zone: string
}) {
  const main = channel.main_media
  const sub = channel.sub_media
  const observed = main?.observed_at ?? sub?.observed_at
  if (!main && !sub) return <span className='text-muted-foreground'>—</span>
  const mainParts = main ? streamLine(main, true) : []
  const subParts = sub ? streamLine(sub, false) : []
  return (
    <div
      className='flex items-center gap-1.5'
      title={detailTitle(main, sub, observed, zone) || undefined}
    >
      <div className='grid gap-0.5 leading-tight'>
        {mainParts.length ? (
          <span className='font-medium whitespace-nowrap'>
            {mainParts.map((part, index) => (
              <span key={`${part}-${index}`}>
                {index > 0 && (
                  <span className='mx-1 text-zinc-600'>·</span>
                )}
                {part}
              </span>
            ))}
          </span>
        ) : (
          <span className='text-zinc-600'>—</span>
        )}
        {subParts.length ? (
          <span className='text-[10px] whitespace-nowrap text-zinc-500'>
            子: {subParts.join(' · ')}
          </span>
        ) : null}
      </div>
      <AudioPill media={main ?? sub} />
    </div>
  )
}
