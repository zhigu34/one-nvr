import { createFileRoute } from '@tanstack/react-router'
import { StoragePools } from '@/features/storage-pools'

export const Route = createFileRoute('/_authenticated/storage-pools/')({
  component: StoragePools,
})
