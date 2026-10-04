import { createFileRoute, redirect } from '@tanstack/react-router'
import { apiRequest } from '@/lib/api-client'
import type { SetupStatus } from '@/lib/types'
import { Login } from '@/features/setup/login'

export const Route = createFileRoute('/sign-in')({
  beforeLoad: async () => {
    const status = await apiRequest<SetupStatus>('/api/v1/setup/status')
    if (!status.initialized) throw redirect({ to: '/setup' })
  },
  component: Login,
})
