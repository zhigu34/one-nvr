import type { QueryClient } from '@tanstack/react-query'
import {
  createRootRouteWithContext,
  Outlet,
  Link,
} from '@tanstack/react-router'
import { Toaster } from '@/components/ui/sonner'

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()(
  {
    component: () => (
      <>
        <Outlet />
        <Toaster />
      </>
    ),
    notFoundComponent: () => (
      <main className='p-10'>
        <h1>页面不存在</h1>
        <Link to='/'>返回总览</Link>
      </main>
    ),
    errorComponent: () => (
      <main role='alert' className='p-10'>
        页面加载失败，请刷新重试。
      </main>
    ),
  }
)
