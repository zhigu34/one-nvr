import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from '@tanstack/react-router'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import { RecordingsWorkspace } from './index'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})

const CH = '00000000-0000-4000-8000-000000000001'

// One entry holds both views: the tab strip must switch between them without
// losing the channel and time context the other view is looking at.
test('the recordings entry switches between playback and file management', async () => {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  calls.request.mockImplementation(async (path: string) => {
    if (path === '/api/v1/channels?limit=100')
      return {
        items: [
          {
            id: CH,
            channel_no: 1,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 1,
            permissions: ['playback'],
          },
        ],
        next_cursor: null,
      }
    if (path === '/api/v1/site')
      return {
        id: '00000000-0000-4000-8000-0000000000f1',
        name: '站点',
        timezone: 'Asia/Shanghai',
        channel_count: 16,
        require_storage_write_proof: true,
        recording_retention_days: 0,
        version: 1,
      }
    if (path.startsWith('/api/v1/recordings/stats'))
      return {
        segments: 0,
        ready: 0,
        anomalous: 0,
        protected: 0,
        deleted: 0,
        duration_ms: 0,
        logical_bytes: 0,
        deleted_bytes: 0,
        oldest_start: null,
        newest_end: null,
      }
    if (path.startsWith('/api/v1/recordings?'))
      return { items: [], next_cursor: null }
    if (path.includes('/source-revisions'))
      return { items: [], next_cursor: null }
    if (path.startsWith('/api/v1/storage-pools'))
      return { items: [], next_cursor: null }
    throw new Error('Unexpected query ' + path)
  })
  // The playback panel links back to the live page, so the workspace needs a
  // router in scope even when the management tab is the one selected.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <RecordingsWorkspace initialChannel={CH} initialTab='files' />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  const view = await render(<RouterProvider router={router} />)
  await expect.element(view.getByRole('heading', { name: '录像' })).toBeVisible()
  const tabs = view.getByRole('tab')
  await expect.element(tabs).toHaveLength(2)
  // The deep link selected the management tab; the channel it carried is the
  // one that tab is filtering on.
  await expect
    .element(view.getByRole('tab', { name: '录像管理' }))
    .toHaveAttribute('aria-selected', 'true')
  await expect
    .element(view.getByRole('tab', { name: '回放' }))
    .toHaveAttribute('aria-selected', 'false')
  await userEvent.click(view.getByRole('tab', { name: '回放' }))
  await expect
    .element(view.getByRole('tab', { name: '回放' }))
    .toHaveAttribute('aria-selected', 'true')
  // Both panels stay mounted so switching cannot discard loaded data; the
  // inactive one is hidden rather than unmounted, so it leaves the
  // accessibility tree while its queries and selection survive.
  const panels = document.querySelectorAll('[role="tabpanel"]')
  expect(panels).toHaveLength(2)
  expect((panels[0] as HTMLElement).hidden).toBe(false)
  expect((panels[1] as HTMLElement).hidden).toBe(true)
})
