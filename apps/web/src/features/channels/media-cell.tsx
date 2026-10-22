import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
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
 * The audio verdict for one stream, at the end of its line.
 *
 * The codec is NOT part of this label any more. It used to read "主 PCMA",
 * which repeated the stream marker that already starts the line and pushed the
 * codec in front of the parameters an operator scans for. The codec lives in
 * the hover title with the sample rate and channel count; the pill answers only
 * the question it is placed to answer — does this stream carry sound.
 *
 * null means the test predates audio capture, which is different from "the
 * camera sent no audio"; saying 无音 for both made a stale probe look like a
 * silent camera.
 */
function AudioPill({
  codec,
  media,
  stream,
}: {
  codec: string | null | undefined
  media: Media
  stream: '主' | '子'
}) {
  if (codec == null) {
    return (
      <span
        title={`${stream}流：这次测试早于音频采集，需要重新测试才能判断`}
        className='rounded border border-border/60 px-1.5 py-px text-[10px] leading-4 text-muted-foreground/60'
      >
        未测
      </span>
    )
  }
  if (codec === '') {
    return (
      <span
        title={`${stream}流：摄像头没有送音频`}
        className='rounded border border-border/60 px-1.5 py-px text-[10px] leading-4 text-muted-foreground/50'
      >
        无音
      </span>
    )
  }
  const label = audioCodecLabel(codec)
  const detail = audioDetail(media)
  const preview = audioPreviewable(codec)
    ? '实时预览可出声'
    : '实时预览无声（WebRTC 只转发 opus/PCMA/PCMU）'
  const title = [`${stream}流音频：${label}`, detail, preview]
    .filter(Boolean)
    .join(' · ')
  return (
    <span
      title={title}
      className={
        preview
          ? 'rounded border border-emerald-800/50 bg-emerald-950/40 px-1.5 py-px text-[10px] leading-4 text-emerald-400'
          : 'rounded border border-amber-800/50 bg-amber-950/30 px-1.5 py-px text-[10px] leading-4 text-amber-400'
      }
    >
      {label}
    </span>
  )
}

/**
 * One line per stream: resolution, codec, frame rate.
 *
 * Two things are deliberately absent.
 *
 * The codec PROFILE: "Main" is the worst possible word here — it reads as the
 * main stream, which is exactly what the leading badge on this line already
 * means — and it cluttered every row with a compression detail nobody scans for.
 *
 * The BIT RATE: the table already has a 码率 column, so printing it here said the
 * same number twice and pushed the line past the column's width, which turned
 * the frame rate into "20.08f…". Both stay in the hover title, which is where
 * the full technical picture lives.
 */
function streamParts(media: Media, withMegapixels: boolean) {
  const resolution =
    media.width && media.height ? `${media.width}×${media.height}` : ''
  // The shorthand leads, and the exact pixel count follows it: "4MP" alone hides
  // that a 4MP sensor is actually streaming 704×576.
  const shorthand = withMegapixels ? megapixel(media.width, media.height) : ''
  return [shorthand, resolution, codecLabel(media.codec), fpsLabel(media)].filter(
    Boolean
  )
}

/** The full technical picture, shown on hover. Absent facts are omitted. */
function detailTitle(
  main: Media | null | undefined,
  sub: Media | null | undefined,
  observed: string | null | undefined,
  zone: string
) {
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
 * Two lines, one per stream, each opening with its own badge. The previous
 * layout printed the stream identity three different ways — a 主/子 prefix, a
 * "主 PCMA" audio pill, and an unbounded parameter list — so the line that read
 * "4MP · 2560×1440 · H.264 · Main · High · 25fps · 4.1M" needed the reader to
 * know that "Main" was a profile and not a stream. One badge names the stream;
 * everything after it is a parameter; the audio verdict sits at the end of the
 * line so the two lines end together.
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
  return (
    <div
      className='grid gap-1'
      title={detailTitle(main, sub, observed, zone) || undefined}
    >
      <StreamLine
        badge='主'
        parts={main ? streamParts(main, false) : []}
        audio={main ? <AudioPill codec={main.audio_codec} media={main} stream='主' /> : null}
        emphasis
      />
      {(sub || main) && (
        <StreamLine
          badge='子'
          parts={sub ? streamParts(sub, false) : []}
          audio={sub ? <AudioPill codec={sub.audio_codec} media={sub} stream='子' /> : null}
        />
      )}
    </div>
  )
}

/**
 * One stream's line: a badge, its parameters, then the audio verdict pushed to
 * the right edge. `w-full` on the parameter span is what makes the two lines'
 * trailing audio marks line up instead of following their text.
 */
function StreamLine({
  badge,
  parts,
  audio,
  emphasis,
}: {
  badge: '主' | '子'
  parts: string[]
  audio: ReactNode
  emphasis?: boolean
}) {
  return (
    <div className='flex min-w-0 items-center gap-1.5'>
      <span
        aria-hidden
        className={cn(
          'shrink-0 rounded border px-1 py-px text-[10px] leading-4',
          emphasis
            ? 'border-border bg-muted text-foreground'
            : 'border-border/60 text-muted-foreground'
        )}
      >
        {badge}
      </span>
      {parts.length ? (
        <span
          className={cn(
            // The parameters are the flexible part of the line: when the column
            // is narrow they give way first, so the audio pill keeps its place at
            // the right edge instead of being overlapped.
            'min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap',
            emphasis
              ? 'text-xs text-foreground'
              : 'font-mono text-xs text-muted-foreground'
          )}
        >
          {parts.join(' · ')}
        </span>
      ) : (
        <span className='text-xs text-muted-foreground/60'>—</span>
      )}
      <span className='shrink-0'>{audio}</span>
    </div>
  )
}
