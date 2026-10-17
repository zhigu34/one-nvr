import { createFileRoute, redirect } from '@tanstack/react-router'

// Storage pools now live inside the storage workspace, next to archive targets.
// The old address is kept as a redirect so existing bookmarks and any external
// links do not 404.
export const Route = createFileRoute('/_authenticated/storage-pools/')({
  beforeLoad: () => {
    throw redirect({ to: '/storage', search: { tab: 'pools' } })
  },
})
