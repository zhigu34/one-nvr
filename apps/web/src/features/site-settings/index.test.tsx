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
// five payloads races the query cache against the assertions.
const MODULE_STATES: {
  name: string
  state: Schema<'Capability'>['state']
  reason: string
  label: string
}[] = [
  { name: 'switch off reads as not enabled', state: 'disabled', reason: 'feature_disabled', label: '功能未启用' },
  { name: 'enabled but unconfigured asks for configuration', state: 'not_configured', reason: 'detection_not_configured', label: '待配置' },
  { name: 'configured with answering dependencies is running', state: 'healthy', reason: 'probe_succeeded', label: '运行中' },
  { name: 'a dead dependency is a running fault, not a missing feature', state: 'unavailable', reason: 'probe_failed', label: '运行异常' },
  { name: 'a module the build lacks says so', state: 'not_available', reason: 'implementation_pending', label: '版本尚未提供' },
]

for (const item of MODULE_STATES) {
  test(item.name, async () => {
    const { view } = fixture(true, {
      enabled: item.state !== 'disabled',
      implemented: item.state !== 'not_available',
      state: item.state,
      reason: item.reason,
    })
    const screen = await render(view)
    await expect
      .element(screen.getByTestId('intelligence-status'))
      .toHaveTextContent(item.label)
  })
}

// "已启用未配置" is only useful if it tells the operator where to go; the PRD
// asks for guidance, not just a state label.
test('a pending module links to the page that configures it', async () => {
  const { view } = fixture(true, {
    enabled: true,
    implemented: true,
    state: 'not_configured',
    reason: 'detection_not_configured',
  })
  const screen = await render(view)
  await expect
    .element(screen.getByTestId('intelligence-status'))
    .toHaveTextContent('待配置')
  const link = screen.getByRole('link', { name: '去配置检测' })
  await expect.element(link).toBeVisible()
  expect(link.element().getAttribute('href')).toContain('/detection')
})

// A disabled module is not a configuration problem, so it must not push a
// configuration link at the operator.
test('a disabled module offers no configuration link', async () => {
  const { view } = fixture(true)
  const screen = await render(view)
  await expect
    .element(screen.getByTestId('intelligence-status'))
    .toHaveTextContent('功能未启用')
  expect(document.querySelector(`[data-testid='intelligence-status'] a`)).toBeNull()
})
