import { createFileRoute } from '@tanstack/react-router'
import { RecordingsWorkspace } from '@/features/recordings-workspace'

export const Route = createFileRoute('/_authenticated/recordings/')({
  // Deep links and the instant-playback entry only prefill the workspace; they
  // never start media on their own (PLAY-09). `tab` selects playback or file
  // management without dropping the channel and time context.
  validateSearch: (search: Record<string, unknown>) => ({
    channel: typeof search.channel === 'string' ? search.channel : '',
    at: typeof search.at === 'string' ? search.at : '',
    tab: typeof search.tab === 'string' ? search.tab : '',
  }),
  component: function RecordingsPage() {
    const { channel, at, tab } = Route.useSearch()
    return (
      <RecordingsWorkspace initialChannel={channel} initialAt={at} initialTab={tab} />
    )
  },
})
