import { createFileRoute, redirect } from '@tanstack/react-router'
import { apiRequest } from '@/lib/api-client'
import type { SetupStatus } from '@/lib/types'
import { Setup } from '@/features/setup'

export const Route = createFileRoute('/setup')({
  beforeLoad: async () => {
    const status = await apiRequest<SetupStatus>('/api/v1/setup/status')
    if (status.initialized) throw redirect({ to: '/sign-in' })
  },
  component: Setup,
})
