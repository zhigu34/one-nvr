import { createFileRoute } from '@tanstack/react-router'
import { StorageWorkspace, type StorageTab } from '@/features/storage-workspace'

export const Route = createFileRoute('/_authenticated/storage/')({
  // The tab is part of the address, not local state: a refresh must land on the
  // tab the operator is looking at, and a link to the archive targets must open
  // them. A missing value normalises to 'pools' here rather than to '', so the
  // address bar never shows a bare `?tab=`.
  validateSearch: (search: Record<string, unknown>): { tab: StorageTab } => ({
    tab: search.tab === 'archive' ? 'archive' : 'pools',
  }),
  component: function StoragePage() {
    const { tab } = Route.useSearch()
    const navigate = Route.useNavigate()
    return (
      <StorageWorkspace
        tab={tab}
        onTabChange={(next) =>
          // `replace` keeps the tab switch out of the history stack: flipping
          // between the two panels is not navigation the back button should
          // replay one step at a time.
          void navigate({ search: { tab: next }, replace: true })
        }
      />
    )
  },
})
