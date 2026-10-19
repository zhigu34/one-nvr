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

/**
 * Codec profiles in the words an operator reasons about.
 *
 * "Main" on its own is the worst possible label here: it reads as the MAIN
 * stream, which is exactly the confusion the two lines above it are trying to
 * avoid. The profile is a compression profile, not a stream identity.
 */
const VIDEO_PROFILE_NAMES: Record<string, string> = {
  baseline: 'Baseline',
  'constrained baseline': 'Baseline',
  main: 'Main',
  high: 'High',
  'main 10': 'Main10',
  'high 10': 'High10',
}

function profileLabel(profile: string | undefined) {
  if (!profile) return ''
  return VIDEO_PROFILE_NAMES[profile.toLowerCase()] ?? profile
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
 * One audio mark per stream, naming WHICH stream it belongs to.
 *
 * The previous version showed a single pill for "the row", so a channel with
 * audio on the main stream and none on the sub looked identical to one with audio
 * on both. That is the question an operator asks first when preview plays sound
 * but recording does not — the recording follows the main stream, the preview may
 * follow the sub — and the cell could not answer it.
 *
 * Codes are spelled out with their sample rate, channel count and bit rate on
 * hover. A stream the probe confirmed has NO audio prints 无音 rather than
 * nothing: silence made "not captured yet" and "genuinely silent" look alike.
 */
function AudioMark({
  stream,
  codec,
  media,
}: {
  stream: '主' | '子'
  codec: string | null | undefined
  media: Media
}) {
  // null means the test predates audio capture — say so rather than guessing.
  const unknown = codec == null
  if (unknown) {
    return (
      <span
        title={`${stream}流：这次测试早于音频采集，需要重新测试才能判断`}
        className='rounded-full border border-zinc-700/60 bg-zinc-900/60 px-1.5 py-px text-[10px] leading-4 text-zinc-500'
      >
        {stream}?
      </span>
    )
  }
  if (codec === '') {
    return (
      <span
        title={`${stream}流：摄像头没有送音频`}
        className='rounded-full border border-zinc-700/60 bg-zinc-900/60 px-1.5 py-px text-[10px] leading-4 text-zinc-500'
      >
        {stream}无音
      </span>
    )
  }
  const label = audioCodecLabel(codec)
  const detail = audioDetail(media)
  const preview = audioPreviewable(codec)
    ? '实时预览可出声'
    : '实时预览无声（WebRTC 只转发 opus/PCMA/PCMU）'
  const title = [
    `${stream}流音频：${label}`,
    detail,
    preview,
  ].filter(Boolean).join(' · ')
  const tone = preview
    ? 'border-emerald-700/40 bg-emerald-950/40 text-emerald-400'
    : 'border-amber-700/40 bg-amber-950/30 text-amber-400'
  return (
    <span
      title={title}
      className={`rounded-full border px-1.5 py-px text-[10px] leading-4 ${tone}`}
    >
      {stream} {label}
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
    profileLabel(media.profile),
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
      <div className='flex shrink-0 flex-col gap-0.5'>
        {main && (
          <AudioMark stream='主' codec={main.audio_codec} media={main} />
        )}
        {sub && <AudioMark stream='子' codec={sub.audio_codec} media={sub} />}
      </div>
    </div>
  )
}
