import { createFileRoute } from '@tanstack/react-router'
import { LivePreview } from '@/features/live'

export const Route = createFileRoute('/_authenticated/live/')({
  component: LivePreview,
})
