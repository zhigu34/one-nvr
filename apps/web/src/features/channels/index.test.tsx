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
function observed(
  state: Schema<'ObservedStatus'>['state'],
  reason = ''
): Schema<'ObservedStatus'> {
  return { state, reason, observed_at: null, expires_at: null }
}
const summaries: Schema<'ChannelSummary'>[] = [
  {
    channel_id: '00000000-0000-4000-8000-000000000001',
    channel_no: 1,
    channel_name: '大门',
    channel_group: '一层',
    enabled: true,
    version: 3,
    permissions: ['configure', 'live'],
    current_revision_id: '00000000-0000-4000-8000-0000000000aa',
    source_ip: '192.168.66.114',
    main_path: '/Streaming/Channels/101',
    sub_path: '/Streaming/Channels/102',
    main: observed('healthy'),
    sub: observed('unknown', 'observation_missing'),
    recording: observed('disabled', 'recording_disabled'),
    last_error: '',
    bitrate_kbps: 4096,
    updated_at: '2026-10-08T02:30:00Z',
  },
  {
    channel_id: '00000000-0000-4000-8000-000000000002',
    channel_no: 2,
    channel_name: '后门',
    channel_group: '外围',
    enabled: true,
    version: 1,
    permissions: ['configure', 'live'],
    current_revision_id: null,
    main: observed('not_configured', 'source_not_configured'),
    sub: observed('not_configured', 'source_not_configured'),
    recording: observed('disabled', 'source_not_configured'),
    last_error: 'source_unavailable',
    bitrate_kbps: null,
    updated_at: null,
  },
  {
    channel_id: '00000000-0000-4000-8000-000000000003',
    channel_no: 3,
    channel_name: '只读通道',
    channel_group: '',
    enabled: true,
    version: 1,
    permissions: ['live'],
    current_revision_id: null,
    main: observed('not_configured', 'source_not_configured'),
    sub: observed('not_configured', 'source_not_configured'),
    recording: observed('disabled', 'source_not_configured'),
    last_error: '',
    bitrate_kbps: null,
    updated_at: null,
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
  calls.request.mockImplementation(
    async (path: string, init?: RequestInit) => {
      if (init?.method === 'PATCH') return summaries[0]
      if (path.endsWith('/source-config-export'))
        return {
          format_version: 1,
          site_id: 'site',
          exported_at: new Date().toISOString(),
          channels: [],
        }
      if (path.endsWith('/channels/summary')) return { items: summaries }
      if (path.endsWith('/api/v1/site'))
        return {
          id: 'site',
          name: '站点',
          timezone: 'Asia/Shanghai',
          channel_count: 16,
          version: 1,
        }
      throw new Error('Unexpected query ' + path)
    }
  )
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
// Every column comes from the one summary response; the page must not fall back
// to per-channel polling.
test('the list is drawn from a single summary request', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await expect.element(view.getByTestId('channel-row')).toHaveLength(3)
  await expect
    .poll(() =>
      calls.request.mock.calls.filter(([path]) =>
        path.includes('/source/status')
      ).length
    )
    .toBe(0)
  await expect
    .element(view.getByTestId('channel-row').first())
    .toHaveTextContent('4.1 Mbps')
  setup.client.clear()
})
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
test('the group filter narrows by business grouping', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.selectOptions(view.getByLabelText('分组筛选'), '外围')
  await expect.element(view.getByTestId('channel-row')).toHaveLength(1)
  await expect
    .element(view.getByTestId('channel-row'))
    .toHaveTextContent('后门')
  await userEvent.selectOptions(view.getByLabelText('分组筛选'), 'none')
  await expect.element(view.getByTestId('channel-row')).toHaveLength(1)
  await expect
    .element(view.getByTestId('channel-row'))
    .toHaveTextContent('只读通道')
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
    channel_ids: [summaries[1].channel_id],
  })
  await expect.element(view.getByRole('status')).toHaveTextContent('配置已导出')
  setup.client.clear()
})
test('revoked selection never broadens export to every other channel', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('checkbox', { name: '选择 CH02' }))
  setup.client.setQueryData(['/api/v1/channels/summary'], {
    items: summaries.map((channel) =>
      channel.channel_no === 2 ? { ...channel, permissions: ['live'] } : channel
    ),
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
// CH-04: disabling is an explicit, confirmed, reversible action that leaves the
// source, the recording policy and the history alone.
test('disabling a channel is confirmed and sends only the enabled flag', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(
    view.getByRole('button', { name: '停用 CH01', exact: true })
  )
  await userEvent.click(view.getByRole('button', { name: '停用', exact: true }))
  await expect
    .poll(() => calls.request.mock.calls.filter(([, init]) => init?.method === 'PATCH').length)
    .toBe(1)
  const [path, init] = calls.request.mock.calls.find(
    ([, value]) => value?.method === 'PATCH'
  )!
  expect(path).toBe(`/api/v1/channels/${summaries[0].channel_id}`)
  expect(JSON.parse(init.body as string)).toEqual({ enabled: false })
  expect(new Headers(init.headers).get('If-Match')).toBe('"3"')
  setup.client.clear()
})
test('batch grouping patches every selected channel with its own version', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('checkbox', { name: '选择 CH01' }))
  await userEvent.click(view.getByRole('checkbox', { name: '选择 CH02' }))
  await userEvent.fill(view.getByLabelText('批量分组'), '机房 A')
  await userEvent.click(
    view.getByRole('button', { name: '应用到选中 2 路', exact: true })
  )
  await expect
    .poll(
      () =>
        calls.request.mock.calls.filter(([, init]) => init?.method === 'PATCH')
          .length
    )
    .toBe(2)
  const patches = calls.request.mock.calls.filter(
    ([, init]) => init?.method === 'PATCH'
  )
  expect(patches.map(([, init]) => JSON.parse(init.body as string))).toEqual([
    { channel_group: '机房 A' },
    { channel_group: '机房 A' },
  ])
  expect(
    patches.map(([, init]) => new Headers(init.headers).get('If-Match'))
  ).toEqual(['"3"', '"1"'])
  setup.client.clear()
})
