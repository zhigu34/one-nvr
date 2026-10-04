import { createFileRoute } from '@tanstack/react-router'
import { Overview } from '@/features/foundation/overview'

export const Route = createFileRoute('/_authenticated/')({
  component: Overview,
})
