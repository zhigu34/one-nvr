import type { Schema } from '@/lib/types'
import { formatMinute } from '@/features/playback/zone'
import { audioCodecLabel, audioPreviewable } from './media'

type Media = Schema<'ChannelMediaParams'>

// Megapixel shorthand: 2560×1920 → "4MP", 1920×1080 → "2MP". Ties round down
// (1440p → 2MP) the way camera vendors label them; unknown sizes keep the
// full resolution instead of an invented round figure.
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

// The audio pill: only rendered when the probe actually captured a codec.
// "No audio" stays silent — printing it on every row was pure noise.
function AudioPill({ codec }: { codec: string | null | undefined }) {
  if (codec == null || codec === '') return null
  const label = audioCodecLabel(codec)
  if (audioPreviewable(codec))
    return (
      <span
        title='实时预览可出声'
        className='rounded-full border border-emerald-700/40 bg-emerald-950/40 px-1.5 py-px text-[10px] leading-4 text-emerald-400'
      >
        {label}
      </span>
    )
  return (
    <span
      title='实时预览无声：ZLM 的 WebRTC 只支持 opus / PCMA / PCMU，其余编码（如 AAC）不会被转发'
      className='rounded-full border border-amber-700/40 bg-amber-950/30 px-1.5 py-px text-[10px] leading-4 text-amber-400'
    >
      {label}
    </span>
  )
}

// Media parameters are the applied revision's newest successful test result.
// They are reference information, so they keep showing while the camera is
// unreachable, and the hover title tells the operator how old they are.
// Visual order: the main stream as one bold badge line, the sub stream in a
// smaller dimmed line beneath, audio as a pill on the right.
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
  const mp = megapixel(main?.width, main?.height)
  const mainParts = [mp, codecLabel(main?.codec), fpsLabel(main ?? ({} as Media))]
    .filter(Boolean)
  const subParts = [
    sub?.width && sub?.height ? `${sub.width}×${sub.height}` : '',
    fpsLabel(sub ?? ({} as Media)),
  ].filter(Boolean)
  return (
    <div
      className='flex items-center gap-1.5'
      title={
        observed && zone
          ? `最近观测：${formatMinute(Date.parse(observed), zone)}`
          : undefined
      }
    >
      <div className='grid gap-0.5 leading-tight'>
        {mainParts.length ? (
          <span className='font-medium whitespace-nowrap'>
            {mainParts.map((part, index) => (
              <span key={part}>
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
      <AudioPill codec={main?.audio_codec ?? sub?.audio_codec ?? null} />
    </div>
  )
}
