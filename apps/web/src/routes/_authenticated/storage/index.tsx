import { createFileRoute } from '@tanstack/react-router'
import { StorageWorkspace } from '@/features/storage-workspace'

export const Route = createFileRoute('/_authenticated/storage/')({
  // `tab` keeps old links working: /storage-pools and /archive-targets redirect
  // here with the tab already selected.
  validateSearch: (search: Record<string, unknown>) => ({
    tab: typeof search.tab === 'string' ? search.tab : '',
  }),
  component: function StoragePage() {
    const { tab } = Route.useSearch()
    return <StorageWorkspace initialTab={tab} />
  },
})
