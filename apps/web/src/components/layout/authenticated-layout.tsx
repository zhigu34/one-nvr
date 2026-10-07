import { useEffect } from 'react'
import { Outlet } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import { getCookie } from '@/lib/cookies'
import type { Schema } from '@/lib/types'
import { attachUserActivity } from '@/lib/user-activity'
import { LayoutProvider } from '@/context/layout-provider'
import { SearchProvider } from '@/context/search-provider'
import { useTheme } from '@/context/theme-provider'
import { Button } from '@/components/ui/button'
import { SidebarInset, SidebarProvider } from '@/components/ui/sidebar'
import { useAPI } from '@/features/foundation/hooks'
import { AppSidebar } from './app-sidebar'
import { Header } from './header'
import { Main } from './main'

export function AuthenticatedLayout() {
  const session = useAPI<Schema<'Session'>>('/api/v1/auth/me', true, 5000)
  useEffect(
    () =>
      attachUserActivity(document, () => {
        void apiRequest('/api/v1/auth/activity', jsonRequest('POST')).catch(
          () => {}
        )
      }),
    []
  )
  const setUser = useAuthStore((s) => s.setUser)
  useEffect(() => {
    if (session.data) setUser(session.data.user)
  }, [session.data, setUser])
  const { theme, setTheme } = useTheme()
  return (
    <SearchProvider>
      <LayoutProvider>
        <SidebarProvider defaultOpen={getCookie('sidebar_state') !== 'false'}>
          <AppSidebar />
          <SidebarInset>
            <Header fixed>
              <span className='text-sm text-muted-foreground'>
                本地视频管理
              </span>
              <Button
                className='ml-auto'
                variant='ghost'
                onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}
              >
                切换主题
              </Button>
            </Header>
            <Main id='content'>
              <Outlet />
            </Main>
          </SidebarInset>
        </SidebarProvider>
      </LayoutProvider>
    </SearchProvider>
  )
}
