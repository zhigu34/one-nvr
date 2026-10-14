import { createFileRoute } from '@tanstack/react-router'
import { LivePreview } from '@/features/live'

export const Route = createFileRoute('/_authenticated/live/')({
  // `channel` pre-fills the first grid slot (from the channels page preview
  // shortcut); it only pre-fills — it never starts media on its own.
  validateSearch: (search: Record<string, unknown>) => ({
    channel: typeof search.channel === 'string' ? search.channel : '',
  }),
  component: function LivePage() {
    const { channel } = Route.useSearch()
    return <LivePreview initialChannel={channel} />
  },
})
