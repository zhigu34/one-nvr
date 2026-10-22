import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import type { Schema } from '@/lib/types'
import { Settings } from './index'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})
function fixture(
  requireProof: boolean,
  modules: Schema<'Capability'> = {
    enabled: false,
    implemented: true,
    state: 'disabled',
    reason: 'feature_disabled',
  }
) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  const mutations: { path: string; body: unknown }[] = []
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method === 'PATCH') {
      mutations.push({
        path,
        body: init.body ? JSON.parse(init.body as string) : undefined,
      })
      return {
        id: '00000000-0000-4000-8000-000000000001',
        name: '站点',
        timezone: 'Asia/Shanghai',
        channel_count: 16,
        require_storage_write_proof: !requireProof,
        event_clip_lead_seconds: 5,
        event_clip_trail_seconds: 10,
        recording_retention_days: 0,
        version: 2,
      } satisfies Schema<'Site'>
    }
    if (path === '/api/v1/site')
      return {
        id: '00000000-0000-4000-8000-000000000001',
        name: '站点',
        timezone: 'Asia/Shanghai',
        channel_count: 16,
        require_storage_write_proof: requireProof,
        event_clip_lead_seconds: 5,
        event_clip_trail_seconds: 10,
        recording_retention_days: 0,
        version: 1,
      } satisfies Schema<'Site'>
    // Timezones, capabilities and hardware are secondary on this page.
    if (path === '/api/v1/timezones')
      return [{ id: 'Asia/Shanghai', label: '上海', current_time: '' }]
    if (path === '/api/v1/capabilities')
      return {
        frigate: modules,
        cloud_archive: { ...modules, reason: 'archive_target_missing' },
      }
    return null
  })
  // A pending module offers a link to its configuration page, so the page needs
  // a router even though these tests never navigate.
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={new QueryClient()}>
        <Settings />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return { mutations, view: <RouterProvider router={router} /> }
}

// The setting is the one place a site may trade reliability for convenience, so
// the panel must state what it costs rather than offering a bare toggle.
test('write proof on explains that recording is proven before it starts', async () => {
  const setup = fixture(true)
  const view = await render(setup.view)
  await expect
    .element(view.getByText(/先真实写入并校验一段录像/))
    .toBeVisible()
  await expect
    .element(view.getByRole('button', { name: '关闭写入检查' }))
    .toBeVisible()
})

test('write proof off states the watchdog delay and keeps the capacity check', async () => {
  const setup = fixture(false)
  const view = await render(setup.view)
  await expect
    .element(view.getByText(/约 3 分钟后停止录像/))
    .toBeVisible()
  // The unchanged half must be visible too, so turning it off does not read as
  // "no storage checks at all".
  await expect
    .element(view.getByText(/两种情况都会继续检查磁盘容量/))
    .toBeVisible()
})

test('turning write proof off sends one PATCH with the expected version', async () => {
  const setup = fixture(true)
  const view = await render(setup.view)
  await userEvent.click(
    view.getByRole('button', { name: '关闭写入检查' })
  )
  expect(setup.mutations).toHaveLength(1)
  expect(setup.mutations[0].path).toBe('/api/v1/site')
  expect(setup.mutations[0].body).toEqual({
    require_storage_write_proof: false,
  })
})

// The four module states are four different things the operator must be able to
// tell apart. Reading 运行异常 as 尚未实现 sends an operator looking for a feature
// that is in fact deployed and merely unreachable — and the reverse claims a
// broken dependency is fine. Each state is its own render: reusing one tree for
// The module status card lived here and was removed: the site settings page is
// for configuring the site, and "is the detector up" is answered by the detection
// page's own runtime card and by the operations page's component table. The
// tests that pinned its wording went with it — keeping them would have been
// asserting a panel nobody renders.
