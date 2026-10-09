import type { Schema } from '@/lib/types'
import { formatMinute } from '@/features/playback/zone'
import { audioCodecLabel, audioPreviewable, videoParamsLabel } from './media'

type Media = Schema<'ChannelMediaParams'>

function AudioCodec({ codec }: { codec: string | null | undefined }) {
  const label = audioCodecLabel(codec)
  if (codec == null || codec === '')
    return <span className='text-muted-foreground'>{label}</span>
  if (audioPreviewable(codec)) return <span>{label}</span>
  return (
    <span
      className='text-amber-700 dark:text-amber-400'
      title='实时预览无声：ZLM 的 WebRTC 只支持 opus / PCMA / PCMU，其余编码（如 AAC）不会被转发'
    >
      {label}
    </span>
  )
}

function StreamParams({
  prefix,
  media,
}: {
  prefix: string
  media: Media | null | undefined
}) {
  if (!media) return <span className='text-muted-foreground'>{prefix} —</span>
  return (
    <span>
      {prefix} {videoParamsLabel(media)}
      {' · 音 '}
      <AudioCodec codec={media.audio_codec} />
    </span>
  )
}

// Media parameters are the applied revision's newest successful test result.
// They are reference information, so they keep showing while the camera is
// unreachable, and the hover title tells the operator how old they are.
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
      className='grid gap-0.5'
      title={
        observed && zone
          ? `最近观测：${formatMinute(Date.parse(observed), zone)}`
          : undefined
      }
    >
      <StreamParams prefix='主' media={main} />
      <StreamParams prefix='子' media={sub} />
    </div>
  )
}
