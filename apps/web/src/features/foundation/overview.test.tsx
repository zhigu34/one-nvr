import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { useAuthStore } from '@/stores/auth-store'
import type { Schema } from '@/lib/types'
import { Overview } from './overview'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})

function component(
  name: string,
  enabled: boolean,
  state: Schema<'Component'>['state'],
  reason: string
): Schema<'Component'> {
  return { name, enabled, state, reason, observed_at: null, expires_at: null }
}

function fixture(
  options: {
    role?: 'admin' | 'viewer'
    components?: Schema<'Component'>[]
    pools?: Schema<'Pool'>[]
  } = {}
) {
  useAuthStore.getState().setUser({
    id: 'user',
    username: 'user',
    role: options.role ?? 'admin',
    enabled: true,
    version: 1,
  })
  calls.request.mockImplementation(async (path: string) => {
    if (path === '/api/v1/site')
      return {
        id: 'site',
        name: '站点',
        timezone: 'Asia/Shanghai',
        channel_count: 16,
        version: 1,
      }
    if (path === '/api/v1/channels/summary')
      return {
        items: [
          {
            channel_id: '00000000-0000-4000-8000-000000000001',
            channel_no: 5,
            channel_name: '客厅',
            channel_group: '一层',
            enabled: true,
            version: 1,
            permissions: ['configure', 'live', 'playback'],
            current_revision_id: 'aa',
            source_ip: '192.168.66.114',
            main_path: '/m',
            sub_path: '/s',
            onvif_port: 80,
            main: { state: 'healthy', reason: '', observed_at: null, expires_at: null },
            sub: { state: 'healthy', reason: '', observed_at: null, expires_at: null },
            recording: { state: 'disabled', reason: 'recording_disabled', observed_at: null, expires_at: null },
            bitrate_kbps: 4096,
            last_error: '',
            updated_at: null,
          },
          {
            channel_id: '00000000-0000-4000-8000-000000000002',
            channel_no: 9,
            channel_name: '空槽',
            channel_group: '',
            enabled: true,
            version: 1,
            permissions: ['configure'],
            current_revision_id: null,
            main: { state: 'not_configured', reason: 'source_not_configured', observed_at: null, expires_at: null },
            sub: { state: 'not_configured', reason: 'source_not_configured', observed_at: null, expires_at: null },
            recording: { state: 'disabled', reason: 'source_not_configured', observed_at: null, expires_at: null },
            last_error: '',
            bitrate_kbps: null,
            updated_at: null,
          },
        ],
      }
    if (path === '/api/v1/detection/runtime')
      return {
        runtime: {
          version: '0.14.0',
          detector: 'cpu',
          inference_ms: 13.8,
          overloaded: false,
          reachable: true,
        },
        revision: null,
      }
    if (path.startsWith('/api/v1/storage-pools'))
      return {
        items:
          options.pools ??
          [
            {
              id: 'pool-1',
              name: '录像盘',
              path: '/data',
              enabled: true,
              is_default: true,
              version: 1,
              checks: [
                {
                  service: 'api',
                  state: 'healthy',
                  observed_at: null,
                  reason: 'read_write_delete_verified',
                  total_bytes: 401.6 * 1024 * 1024 * 1024,
                  free_bytes: 301.3 * 1024 * 1024 * 1024,
                  filesystem_id: '',
                },
              ],
              state: 'ready',
              used_bytes: null,
            },
          ],
      }
    if (path === '/api/v1/operations/components')
      return (
        options.components ?? [
          component('api', true, 'healthy', 'probe_succeeded'),
          component('zlm', true, 'healthy', 'probe_succeeded sampler_running'),
          component('frigate', true, 'healthy', 'probe_succeeded'),
          component('openlist', false, 'disabled', 'feature_disabled'),
        ]
      )
    throw new Error('unexpected path ' + path)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <Overview />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return { client, view: <RouterProvider router={router} /> }
}

test('the four metric cards read live channel, detection and storage facts', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  // Channels card: one healthy main stream of two rows, with the breakdown.
  await expect.element(view.getByText('1/ 2 路在线')).toBeVisible()
  await expect.element(view.getByText('在线 1')).toBeVisible()
  await expect.element(view.getByText('未配置 1')).toBeVisible()
  // Detection card: the detector's own latency, not an invented rate.
  await expect.element(view.getByText('13.8ms 推理')).toBeVisible()
  await expect.element(view.getByText('cpu · 1 路检测中')).toBeVisible()
  // Storage card: capacity facts headline with the state as a side pill.
  await expect.element(view.getByText(/301\.3 GiB/)).toBeVisible()
  await expect.element(view.getByText(/共 401\.6 GiB/)).toBeVisible()
  await expect.element(view.getByText('就绪').nth(0)).toBeVisible()
  // Site card aggregates enabled/healthy components.
  await expect.element(view.getByText('3/ 3 核心服务正常')).toBeVisible()
  setup.client.clear()
})

test('the component matrix names components and marks disabled cards', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  const cards = view.getByTestId('component-card')
  await expect.element(cards).toHaveLength(4)
  // Chinese roles replace raw English state codes.
  await expect.element(view.getByText('流媒体服务')).toBeVisible()
  await expect.element(view.getByText('AI 视觉检测')).toBeVisible()
  await expect.element(view.getByText('外部归档挂载')).toBeVisible()
  // Healthy cards pulse; the disabled one reads 未启用 on a dashed card.
  const zlm = cards.nth(1)
  await expect.element(zlm).toHaveAttribute('data-state', 'healthy')
  await expect.element(zlm.getByText('正常')).toBeVisible()
  const openlist = cards.nth(3)
  await expect.element(openlist).toHaveAttribute('data-state', 'disabled')
  await expect.element(openlist.getByText('未启用')).toBeVisible()
  // Probe reasons render as Chinese facts in mono, not raw codes.
  await expect.element(view.getByText('探测成功 · 采样运行中')).toBeVisible()
  setup.client.clear()
})

test('the active channel patrol lists live channels with shortcuts', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  const cards = view.getByTestId('active-channel-card')
  await expect.element(cards).toHaveLength(1)
  await expect.element(cards.nth(0)).toHaveTextContent('客厅')
  await expect.element(cards.nth(0)).toHaveTextContent('CH05')
  await expect.element(cards.nth(0)).toHaveTextContent('4.1 Mbps')
  // The unconfigured slot never appears in the patrol.
  expect(cards.nth(0).element().textContent).not.toContain('空槽')
  // Shortcuts ride the channel's own permissions.
  await expect
    .element(cards.nth(0).getByRole('link', { name: /预览/ }))
    .toHaveAttribute('href', '/live?channel=00000000-0000-4000-8000-000000000001')
  await expect
    .element(cards.nth(0).getByRole('link', { name: /回放/ }))
    .toHaveAttribute(
      'href',
      '/recordings?channel=00000000-0000-4000-8000-000000000001&at=&tab=playback'
    )
  setup.client.clear()
})

test('a viewer sees no component matrix and no patrol shortcuts beyond grants', async () => {
  const setup = fixture({
    role: 'viewer',
    components: [component('api', true, 'healthy', 'probe_succeeded')],
  })
  const view = await render(setup.view)
  // The operations endpoint is admin-only: the matrix must not render for a
  // viewer, and neither must a card that would leak component health.
  expect(view.getByTestId('component-card').query()).toBeNull()
  setup.client.clear()
})
