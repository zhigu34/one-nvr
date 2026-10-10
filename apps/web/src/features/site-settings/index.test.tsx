import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
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
function fixture(requireProof: boolean) {
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
        version: 1,
      } satisfies Schema<'Site'>
    // Timezones, capabilities and hardware are secondary on this page.
    if (path === '/api/v1/timezones')
      return [{ id: 'Asia/Shanghai', label: '上海', current_time: '' }]
    if (path === '/api/v1/capabilities')
      return {
        frigate: { enabled: false, implemented: false, state: 'disabled', reason: '' },
        cloud_archive: { enabled: false, implemented: false, state: 'disabled', reason: '' },
      }
    return null
  })
  return {
    mutations,
    view: (
      <QueryClientProvider client={new QueryClient()}>
        <Settings />
      </QueryClientProvider>
    ),
  }
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
