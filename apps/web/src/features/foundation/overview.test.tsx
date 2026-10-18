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
    reliability?: unknown[]
    resources?: Record<string, unknown>
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
    if (path === '/api/v1/operations/resources')
      return (
        options.resources ?? {
          cpu_percent: 12.5,
          cpu_known: true,
          cpu_cores: 8,
          memory_percent: 34.2,
          memory_known: true,
          memory_total_bytes: 32 * 1024 ** 3,
          memory_used_bytes: 11 * 1024 ** 3,
          load_average: 1.4,
          load_known: true,
          event_cache: {
            configured: true,
            known: true,
            path: '/cache',
            total_bytes: 512 * 1024 ** 2,
            used_bytes: 128 * 1024 ** 2,
            percent: 25,
          },
          observed_at: new Date().toISOString(),
        }
      )
    if (path.startsWith('/api/v1/reliability/summary'))
      return { items: options.reliability ?? [] }
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

test('the five KPI cards read live channel, detection, storage and host facts', async () => {
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
  // The host card is the fifth KPI: CPU and memory with their bars.
  await expect.element(view.getByTestId('host-resources')).toBeVisible()
  setup.client.clear()
})

test('the component health bar summarises state in one line', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  const bar = view.getByTestId('component-health-bar')
  await expect.element(bar).toBeVisible()
  // A summary number rather than eight boxes: the count is what an operator
  // reads, and three of four enabled components are healthy here.
  await expect.element(bar).toHaveTextContent('3 / 3')
  await expect.element(bar).toHaveTextContent('正常')
  // Every component keeps a pill; the disabled one says so.
  const pills = bar.getByTestId('component-pill')
  await expect.element(pills).toHaveLength(4)
  const disabled = [...document.querySelectorAll('[data-state="disabled"]')]
  expect(disabled.length).toBeGreaterThan(0)
  expect(disabled[0].textContent).toContain('未启用')
  // The detail moved into the tooltip, which is why the bar is one line tall.
  expect(document.querySelectorAll('[data-state="healthy"]').length).toBeGreaterThan(0)
  setup.client.clear()
})

// The old matrix was eight cards tall and repeated the operations page; a
// healthy site should not spend a fifth of the screen saying so.
test('the component matrix is gone, replaced by the bar', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  expect(view.getByTestId('component-card').query()).toBeNull()
  await expect.element(view.getByTestId('component-health-bar')).toBeVisible()
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

// DASH-04: availability and recording coverage over the last day, read from the
// same cross-channel summary the operations page uses so the two cannot
// disagree about a channel.
test('the availability card reports a rate with its own evidence', async () => {
  const setup = fixture({
    reliability: [
      {
        channel_id: 'ch-1',
        channel_no: 1,
        channel_name: '前门',
        kind: 'main',
        healthy_ms: 23 * 3600_000,
        unavailable_ms: 3600_000,
        unknown_ms: 0,
        excluded_ms: 0,
        expected_ms: 0,
        owed_ms: 0,
        covered_ms: 0,
        fault_count: 2,
        observed_ms: 24 * 3600_000,
        sample_sufficient: true,
      },
      {
        channel_id: 'ch-1',
        channel_no: 1,
        channel_name: '前门',
        kind: 'recording',
        healthy_ms: 0,
        unavailable_ms: 0,
        unknown_ms: 0,
        excluded_ms: 0,
        expected_ms: 0,
        owed_ms: 3600_000,
        covered_ms: 1800_000,
        fault_count: 0,
        observed_ms: 24 * 3600_000,
        sample_sufficient: true,
      },
    ],
  })
  const view = await render(setup.view)
  // 23 of 24 hours healthy is 95.8%, and one channel had an interruption.
  await expect.element(view.getByText('95.8%')).toBeVisible()
  await expect.element(view.getByText(/录像覆盖 50.0%/)).toBeVisible()
  await expect.element(view.getByText(/1 路有中断/)).toBeVisible()
})

// A window with no sufficient sample must say so rather than print 0%, which
// would read as "everything was down".
test('the availability card shows a dash when nothing was observed', async () => {
  const setup = fixture({ reliability: [] })
  const view = await render(setup.view)
  await expect.element(view.getByText('窗口内观测不足')).toBeVisible()
})

// Comparing channels is an administrator read, so a viewer gets no card at all
// instead of an empty one.
test('a viewer sees no availability card', async () => {
  const setup = fixture({ role: 'viewer' })
  const view = await render(setup.view)
  expect(view.getByText('24 小时可用性').query()).toBeNull()
})

// DASH-02: the host figures are shown for an administrator.
test('the host resource card reports cpu, memory and load', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await expect.element(view.getByTestId('host-resources')).toBeVisible()
  await expect.element(view.getByText('12.5%')).toBeVisible()
  await expect.element(view.getByText('34.2%')).toBeVisible()
  await expect.element(view.getByText('1.4')).toBeVisible()
})

// A failed read must read "未知", not "0%": zero is indistinguishable from an
// idle healthy host, which is exactly the wrong thing to tell an operator whose
// measurement is broken.
test('an unmeasured host shows 未知 rather than zero', async () => {
  const setup = fixture({
    resources: {
      cpu_percent: 0,
      cpu_known: false,
      cpu_cores: 8,
      memory_percent: 0,
      memory_known: false,
      memory_total_bytes: 0,
      memory_used_bytes: 0,
      load_average: 0,
      load_known: false,
      // Not configured, so the cache reports 未启用 rather than 未知 or 0%:
      // "this deployment did not reserve memory" is its own answer.
      event_cache: {
        configured: false,
        known: false,
        path: '',
        total_bytes: 0,
        used_bytes: 0,
        percent: 0,
      },
      observed_at: new Date().toISOString(),
    },
  })
  const view = await render(setup.view)
  await expect.element(view.getByTestId('host-resources')).toBeVisible()
  expect(view.getByText('未知').elements().length).toBe(3)
  expect(view.getByText('0%').query()).toBeNull()
})

// The patrol's two columns carry their own headings; an outer title saying the
// same thing pushed both down and read as a duplicate.
test('the patrol has no outer title above its two column headings', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  expect(view.getByText('活跃通道与快捷巡检').query()).toBeNull()
  await expect.element(view.getByText('活跃通道巡检')).toBeVisible()
  await expect.element(view.getByText('最近 AI 事件')).toBeVisible()
})

// REC-09: the ring buffer is memory this deployment reserved, so its fill level
// belongs with the host figures. A site that reserved none must read 未启用 —
// showing 0% would claim memory is reserved and idle.
test('the event cache reports its usage and warns near the limit', async () => {
  const setup = fixture({
    resources: {
      cpu_percent: 10,
      cpu_known: true,
      cpu_cores: 8,
      memory_percent: 20,
      memory_known: true,
      memory_total_bytes: 32 * 1024 ** 3,
      memory_used_bytes: 6 * 1024 ** 3,
      load_average: 0.5,
      load_known: true,
      event_cache: {
        configured: true,
        known: true,
        path: '/cache',
        total_bytes: 512 * 1024 ** 2,
        used_bytes: 480 * 1024 ** 2,
        percent: 93.8,
      },
      observed_at: new Date().toISOString(),
    },
  })
  const view = await render(setup.view)
  await expect.element(view.getByTestId('event-cache-detail')).toBeVisible()
  await expect.element(view.getByText(/接近上限/)).toBeVisible()
})

// Not configured is not the same as unmeasured: the page must not report the
// cache as unknown when the deployment deliberately left it off.
test('a deployment without the event cache says 未启用', async () => {
  const setup = fixture({
    resources: {
      cpu_percent: 10,
      cpu_known: true,
      cpu_cores: 8,
      memory_percent: 20,
      memory_known: true,
      memory_total_bytes: 32 * 1024 ** 3,
      memory_used_bytes: 6 * 1024 ** 3,
      load_average: 0.5,
      load_known: true,
      event_cache: {
        configured: false,
        known: false,
        path: '',
        total_bytes: 0,
        used_bytes: 0,
        percent: 0,
      },
      observed_at: new Date().toISOString(),
    },
  })
  const view = await render(setup.view)
  const host = view.getByTestId('host-resources')
  await expect.element(host.getByText('未启用')).toBeVisible()
  await expect
    .element(view.getByTestId('event-cache-detail'))
    .not.toBeInTheDocument()
})
