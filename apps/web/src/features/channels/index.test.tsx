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
import { Channels } from './index'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})
const channels: Schema<'Channel'>[] = [
  {
    id: '00000000-0000-4000-8000-000000000001',
    channel_no: 1,
    channel_name: '大门',
    version: 1,
    permissions: ['configure', 'live'],
  },
  {
    id: '00000000-0000-4000-8000-000000000002',
    channel_no: 2,
    channel_name: '后门',
    version: 1,
    permissions: ['configure', 'live'],
  },
  {
    id: '00000000-0000-4000-8000-000000000003',
    channel_no: 3,
    channel_name: '只读通道',
    version: 1,
    permissions: ['live'],
  },
]
function fixture() {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  calls.request.mockImplementation(async (path: string) => {
    if (path.endsWith('/source-config-export'))
      return {
        format_version: 1,
        site_id: 'site',
        exported_at: new Date().toISOString(),
        channels: [],
      }
    if (path.includes('/source/status')) {
      const channel = channels.find((item) => path.includes(item.id))!
      const configured = channel.channel_no === 1
      const observation = {
        state: configured ? 'healthy' : 'not_configured',
        reason: '',
        observed_at: null,
        expires_at: null,
      }
      return {
        channel_id: channel.id,
        current_revision_id: configured ? 'revision' : null,
        desired_revision_id: null,
        storage_pool_id: null,
        enabled: true,
        version: 1,
        main: observation,
        sub: observation,
        recording: { ...observation, state: 'disabled' },
        requires_initial_recording_mode: !configured,
      }
    }
    return { items: channels, next_cursor: null }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <Channels />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return { client, view: <RouterProvider router={router} /> }
}
test('channel search combines the configured filter without hiding unknown status as empty', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await expect.element(view.getByTestId('channel-row')).toHaveLength(3)
  await userEvent.fill(
    view.getByRole('searchbox', { name: '搜索通道' }),
    'CH01'
  )
  await expect.element(view.getByTestId('channel-row')).toHaveLength(1)
  await expect
    .element(view.getByTestId('channel-row'))
    .toHaveTextContent('大门')
  await userEvent.fill(view.getByRole('searchbox', { name: '搜索通道' }), '')
  await userEvent.selectOptions(view.getByLabelText('配置状态'), 'empty')
  await expect.element(view.getByTestId('channel-row')).toHaveLength(2)
  await userEvent.fill(
    view.getByRole('searchbox', { name: '搜索通道' }),
    '大门'
  )
  await expect.element(view.getByText('没有匹配的通道')).toBeVisible()
  setup.client.clear()
})
test('selected config export contains only the explicitly selected permitted channel', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('checkbox', { name: '选择 CH02' }))
  await expect
    .element(view.getByRole('checkbox', { name: '选择 CH03' }))
    .toBeDisabled()
  await userEvent.click(
    view.getByRole('button', { name: '导出摄像头配置', exact: true })
  )
  const request = calls.request.mock.calls.find(([path]) =>
    path.endsWith('/source-config-export')
  )
  expect(JSON.parse(request![1].body)).toEqual({
    channel_ids: [channels[1].id],
  })
  await expect.element(view.getByRole('status')).toHaveTextContent('配置已导出')
  setup.client.clear()
})

test('revoked selection never broadens export to every other channel', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('checkbox', { name: '选择 CH02' }))
  setup.client.setQueryData(['/api/v1/channels?limit=100'], {
    items: channels.map((channel) =>
      channel.channel_no === 2 ? { ...channel, permissions: ['live'] } : channel
    ),
    next_cursor: null,
  })
  await expect
    .element(view.getByRole('button', { name: '导出摄像头配置' }))
    .toBeDisabled()
  expect(
    calls.request.mock.calls.some(([path]) =>
      path.endsWith('/source-config-export')
    )
  ).toBe(false)
  await userEvent.click(view.getByRole('button', { name: '取消选择' }))
  await expect
    .element(view.getByRole('button', { name: '导出摄像头配置' }))
    .toBeEnabled()
  setup.client.clear()
})
