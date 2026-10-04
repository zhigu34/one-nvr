import { createFileRoute, redirect } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, ApiError } from '@/lib/api-client'
import type { SetupStatus, Schema } from '@/lib/types'
import { AuthenticatedLayout } from '@/components/layout/authenticated-layout'

export const Route = createFileRoute('/_authenticated')({
  beforeLoad: async ({ context }) => {
    const status = await apiRequest<SetupStatus>('/api/v1/setup/status')
    if (!status.initialized) throw redirect({ to: '/setup' })
    try {
      const session = await context.queryClient.fetchQuery({
        queryKey: ['/api/v1/auth/me'],
        queryFn: () => apiRequest<Schema<'Session'>>('/api/v1/auth/me'),
      })
      useAuthStore.getState().setUser(session.user)
    } catch (error) {
      if (error instanceof ApiError && error.status === 401)
        throw redirect({ to: '/sign-in' })
      throw error
    }
  },
  component: AuthenticatedLayout,
})
