import { createFileRoute } from '@tanstack/react-router'
import { RecordingsWorkspace } from '@/features/recordings-workspace'
import { Main } from '@/components/layout/main'

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
      // `fixed` gives this page the layout's real flex chain (grow +
      // overflow-hidden) instead of a viewport-calc guess, so the workspace
      // fills the viewport exactly and never clips or overflows. py-0 drops
      // the default vertical padding — the player page manages its own.
      <Main fixed fluid className='py-0'>
        <RecordingsWorkspace initialChannel={channel} initialAt={at} initialTab={tab} />
      </Main>
    )
  },
})
