import { createFileRoute } from '@tanstack/react-router'
import { ChannelConfigure } from '@/features/channels/configure'

export const Route = createFileRoute('/_authenticated/channels/configure')({
  validateSearch: (search: Record<string, unknown>) => ({
    channel: typeof search.channel === 'string' ? search.channel : '',
    // A deep link may name the tab: the recordings workspace points at the
    // reliability tab when a gap has a matching fault, and landing on 连接配置
    // would make the link look broken.
    tab: typeof search.tab === 'string' ? search.tab : '',
  }),
  component: function ConfigurePage() {
    const { channel, tab } = Route.useSearch()
    return <ChannelConfigure initialId={channel} initialTab={tab} />
  },
})
