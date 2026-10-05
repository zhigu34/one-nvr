import { createFileRoute } from '@tanstack/react-router'
import { Channels } from '@/features/channels'

export const Route = createFileRoute('/_authenticated/channels/')({
  validateSearch: (search: Record<string, unknown>) => ({
    batch: typeof search.batch === 'string' ? search.batch : '',
  }),
  component: function ChannelsPage() {
    const { batch } = Route.useSearch()
    return <Channels initialBatch={batch} />
  },
})
