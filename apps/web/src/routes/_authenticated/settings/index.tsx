import { createFileRoute } from '@tanstack/react-router'
import { Settings } from '@/features/site-settings'

export const Route = createFileRoute('/_authenticated/settings/')({
  component: Settings,
})
