import { createFileRoute } from '@tanstack/react-router'
import { Playback } from '@/features/playback'

export const Route = createFileRoute('/_authenticated/recordings/')({
  // Deep links and the instant-playback entry only prefill the workspace; they
  // never start media on their own (PLAY-09).
  validateSearch: (search: Record<string, unknown>) => ({
    channel: typeof search.channel === 'string' ? search.channel : '',
    at: typeof search.at === 'string' ? search.at : '',
  }),
  component: function PlaybackPage() {
    const { channel, at } = Route.useSearch()
    return <Playback initialChannel={channel} initialAt={at} />
  },
})
