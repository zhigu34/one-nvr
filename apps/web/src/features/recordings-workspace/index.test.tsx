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
const CH2 = '00000000-0000-4000-8000-000000000002'

function mockCommon() {
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
          {
            id: CH2,
            channel_no: 2,
            channel_name: '仓库',
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
        segments: 42,
        ready: 40,
        anomalous: 2,
        protected: 4,
        deleted: 1,
        duration_ms: 9_600_000,
        logical_bytes: 8 * 1024 * 1024 * 1024,
        deleted_bytes: 256 * 1024 * 1024,
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
}

// The workspace is one dark surface: the channels rail, the playback console
// and the file panel all live side by side without a tab switch in between.
test('the recordings workspace renders channels, playback and the file panel side by side', async () => {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  mockCommon()
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <RecordingsWorkspace initialChannel={CH} />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  const view = await render(<RouterProvider router={router} />)
  // The page header row is gone; the live link and the file toggle now live
  // at the end of the playback search toolbar.
  await expect.element(view.getByRole('link', { name: '返回实时预览' })).toBeVisible()
  await expect.element(view.getByTestId('channels-rail')).toBeVisible()
  await expect.element(view.getByRole('button', { name: 'CH01 大门' })).toBeVisible()
  await expect.element(view.getByTestId('playback-console')).toBeVisible()
  // The file rail starts collapsed; the header toggle expands it. Two
  // controls share the label (header chip + collapsed rail strip) — the
  // header one is the first.
  await userEvent.click(view.getByRole('button', { name: '展开文件面板' }).first())
  await expect.element(view.getByTestId('files-panel')).toBeVisible()
  // No tab role is rendered any more; the previous entry split is gone.
  expect(view.getByRole('tab').query()).toBeNull()
})

// Legacy deep links like /recordings?tab=files still resolve, even though the
// URL parameter no longer drives a tab; the page must not throw or refuse.
test('a legacy ?tab=files deep link still mounts the workspace', async () => {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  mockCommon()
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
    history: createMemoryHistory({ initialEntries: ['/?tab=files'] }),
  })
  const view = await render(<RouterProvider router={router} />)
  await userEvent.click(view.getByRole('button', { name: '展开文件面板' }).first())
  await expect.element(view.getByTestId('files-panel')).toBeVisible()
})

// Selecting a different channel from the rail updates the URL state and is
// reflected in the playback region.
test('clicking a channel in the rail switches the active channel', async () => {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  mockCommon()
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <RecordingsWorkspace initialChannel={CH} />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  const view = await render(<RouterProvider router={router} />)
  await expect
    .element(view.getByRole('button', { name: 'CH01 大门' }))
    .toHaveAttribute('aria-pressed', 'true')
  await userEvent.click(view.getByRole('button', { name: 'CH02 仓库' }))
  await expect
    .element(view.getByRole('button', { name: 'CH02 仓库' }))
    .toHaveAttribute('aria-pressed', 'true')
})

// The right panel is admin-only: a viewer must not see file management even
// if they have playback permission.
test('a non-admin viewer does not see the file panel', async () => {
  useAuthStore.getState().setUser({
    id: 'op',
    username: 'operator',
    role: 'operator',
    enabled: true,
    version: 1,
  })
  mockCommon()
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <RecordingsWorkspace initialChannel={CH} />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  const view = await render(<RouterProvider router={router} />)
  await expect.element(view.getByTestId('channels-rail')).toBeVisible()
  await expect.element(view.getByTestId('playback-console')).toBeVisible()
  // No file panel and no toggle button for non-admins; the third grid column
  // is dropped entirely instead of being hidden behind closed state.
  expect(view.getByTestId('files-panel').query()).toBeNull()
  expect(view.getByLabelText('展开文件面板').query()).toBeNull()
})
