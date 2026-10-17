import { createFileRoute, redirect } from '@tanstack/react-router'

// Archive targets now live inside the storage workspace, next to storage pools.
// The old address is kept as a redirect so existing bookmarks do not 404.
export const Route = createFileRoute('/_authenticated/archive-targets/')({
  beforeLoad: () => {
    throw redirect({ to: '/storage', search: { tab: 'archive' } })
  },
})
