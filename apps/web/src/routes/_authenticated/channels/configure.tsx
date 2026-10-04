import { createFileRoute } from '@tanstack/react-router'
import { Forbidden } from '@/features/foundation/ui'

export const Route = createFileRoute('/_authenticated/channels/configure')({
  component: Forbidden,
})
