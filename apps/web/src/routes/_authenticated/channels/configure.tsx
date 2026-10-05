import { createFileRoute } from '@tanstack/react-router'
import { ChannelConfigure } from '@/features/channels/configure'

export const Route = createFileRoute('/_authenticated/channels/configure')({
  validateSearch: (search: Record<string, unknown>) => ({
    channel: typeof search.channel === 'string' ? search.channel : '',
  }),
  component: function ConfigurePage() {
    const { channel } = Route.useSearch()
    return <ChannelConfigure initialId={channel} />
  },
})
