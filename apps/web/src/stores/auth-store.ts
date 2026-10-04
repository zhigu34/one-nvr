import { create } from 'zustand'
import type { components } from '@/lib/api-types'

type User = components['schemas']['User']
export const useAuthStore = create<{
  user: User | null
  setUser: (user: User | null) => void
  reset: () => void
}>((set) => ({
  user: null,
  setUser: (user) => set({ user }),
  reset: () => set({ user: null }),
}))
