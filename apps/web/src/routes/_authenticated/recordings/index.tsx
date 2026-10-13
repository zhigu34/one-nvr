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
      // `fixed` puts this page inside the layout's real flex chain
      // (SidebarInset flex-col → Main grow + overflow-hidden), so the
      // workspace is exactly one viewport tall: top row + timeline band all
      // visible, no page scrolling. py-0 drops the default padding — the
      // player page manages its own spacing.
      <Main fixed fluid className='py-0'>
        <RecordingsWorkspace initialChannel={channel} initialAt={at} initialTab={tab} />
      </Main>
    )
  },
})
