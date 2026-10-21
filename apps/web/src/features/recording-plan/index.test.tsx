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
import { RecordingPlan } from './index'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})

const channel: Schema<'Channel'> = {
  id: '00000000-0000-4000-8000-000000000001',
  channel_no: 1,
  channel_name: '大门',
  channel_group: '',
  enabled: true,
  version: 4,
  permissions: ['configure'],
  configured: true,
  }
const peer: Schema<'Channel'> = {
  ...channel,
  id: '00000000-0000-4000-8000-000000000002',
  channel_no: 2,
  channel_name: '后门',
}

const pool: Schema<'Pool'> = {
  id: '00000000-0000-4000-8000-000000000020',
  name: 'disk1',
  path: '/storage/disk1',
  enabled: true,
  is_default: true,
  version: 1,
  checks: [],
  state: 'ready',
  used_bytes: null,
  warning_percent: 80,
  pressure_percent: 85,
  pressure_target_percent: 80,
  critical_percent: 95,
}

function summary(
  id: string,
  no: number,
  overrides: Record<string, unknown> = {}
): Schema<'ChannelSummary'> {
  return {
    channel_id: id,
    channel_no: no,
    channel_name: no === 1 ? '大门' : '后门',
    channel_group: '',
    enabled: true,
    version: 4,
    permissions: ['configure'],
    current_revision_id: 'rev',
    storage_pool_id: pool.id,
    recording_mode: 'none',
    event_recording_enabled: false,
    source_ip: '192.168.33.20',
    main: { state: 'healthy', reason: '', observed_at: null, expires_at: null },
    sub: {
      state: 'unknown',
      reason: 'observation_missing',
      observed_at: null,
      expires_at: null,
    },
    recording: {
      state: 'disabled',
      reason: 'recording_disabled',
      observed_at: null,
      expires_at: null,
    },
    ...overrides,
  } as Schema<'ChannelSummary'>
}

function fixture(
  options: {
    peer?: boolean
    mode?: string
    eventOn?: boolean
    schedule?: { weekday: number; start_minute: number; end_minute: number }[]
    manual?: boolean
  } = {}
) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  const mutations: { path: string; init: RequestInit }[] = []
  const channels = options.peer ? [channel, peer] : [channel]
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    const method = init?.method && init.method !== 'GET' ? init.method : 'GET'
    if (method !== 'GET') mutations.push({ path, init: init as RequestInit })
    if (path === '/api/v1/site')
      return { timezone: 'Asia/Shanghai', name: '站点' }
    if (path.startsWith('/api/v1/channels/summary'))
      return {
        items: channels.map((entry, index) =>
          summary(entry.id ?? '', entry.channel_no ?? index + 1, {
            recording_mode: options.mode ?? 'none',
            event_recording_enabled: options.eventOn ?? false,
          })
        ),
      }
    if (path.startsWith('/api/v1/channels?'))
      return { items: channels, next_cursor: null }
    if (path.startsWith('/api/v1/storage-pools'))
      return { items: [pool], next_cursor: null }
    if (path.endsWith('/recording-schedule'))
      return {
        channel_id: channel.id,
        windows: options.schedule ?? [],
        timezone: 'Asia/Shanghai',
        active: false,
      }
    if (path.endsWith('/manual-recording')) {
      if (!options.manual) throw new Error('not found')
      return {
        channel_id: channel.id,
        requested_minutes: 30,
        started_at: new Date(Date.now() - 120_000).toISOString(),
        expires_at: new Date(Date.now() + 1_680_000).toISOString(),
        remaining_ms: 1_680_000,
      }
    }
    if (path.endsWith('/recording-policy'))
      return {
        channel_id: channel.id,
        mode: options.mode ?? 'none',
        event_recording_enabled: options.eventOn ?? false,
        version: 4,
      }
    if (path.startsWith('/api/v1/jobs/'))
      return {
        id: 'job-1',
        kind: 'source.policy_apply',
        state: 'succeeded',
        attempts: 1,
      }
    throw new Error('Unexpected query ' + path)
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <RecordingPlan />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return { mutations, client, view: <RouterProvider router={router} /> }
}

// The bar is the only place anything is configured; the table only reports. The
// mode is three segmented buttons rather than a select, because a select hides
// two of the three values behind a click and this page is about comparing them.
test('the bar offers the three modes as buttons and an event switch', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  for (const label of ['关闭录像', '全天录像', '定时录像']) {
    await expect
      .element(view.getByRole('button', { name: label }))
      .toBeVisible()
  }
  await expect
    .element(view.getByRole('button', { name: '事件录像开关' }))
    .toBeVisible()
  // Apply is disabled with nothing selected: a batch of zero is not a batch.
  await expect
    .element(view.getByRole('button', { name: /应用到所选通道/ }))
    .toBeDisabled()
  setup.client.clear()
})

// Selecting one channel echoes its current plan, so editing one channel is the
// same gesture as editing sixteen.
test('selecting a single channel echoes its plan in the bar', async () => {
  const setup = fixture({ mode: 'continuous', eventOn: true })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('选择 CH01'))
  await expect
    .element(view.getByRole('button', { name: '全天录像' }))
    .toHaveAttribute('aria-pressed', 'true')
  await expect
    .element(view.getByRole('button', { name: '事件录像开关' }))
    .toHaveAttribute('aria-pressed', 'true')
  setup.client.clear()
})

// The table is read-only: no selects, no per-row event button, no manual button.
test('the table carries no controls other than the selection checkbox', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  const row = view.getByTestId('policy-row')
  await expect.element(row).toBeVisible()
  // The bar has one select (the manual duration); the ROW must have none.
  expect(row.getByRole('combobox').query()).toBeNull()
  expect(view.getByRole('button', { name: 'CH01 限时录像' }).query()).toBeNull()
  expect(view.getByRole('button', { name: 'CH01 事件录像' }).query()).toBeNull()
  setup.client.clear()
})

// Clicking anywhere on a row selects it: with the controls gone there is nothing
// else to click, and aiming at a checkbox for sixteen rows is busywork.
test('clicking a row selects it', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  const row = view.getByTestId('policy-row')
  await userEvent.click(row)
  await expect.element(row).toHaveAttribute('data-selected', 'true')
  setup.client.clear()
})

// Applying writes the mode for every selected channel, and waits for the switch
// job so a partial batch reports what actually happened.
test('applying writes the mode for each selected channel', async () => {
  const setup = fixture({ peer: true })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('选择 CH01'))
  await userEvent.click(view.getByLabelText('选择 CH02'))
  await userEvent.click(view.getByRole('button', { name: '全天录像' }))
  await userEvent.click(view.getByRole('button', { name: /应用到所选通道/ }))
  await expect
    .poll(
      () =>
        setup.mutations.filter((m) => m.path.endsWith('/recording-policy'))
          .length
    )
    .toBe(2)
  for (const mutation of setup.mutations.filter((m) =>
    m.path.endsWith('/recording-policy')
  )) {
    expect(JSON.parse(mutation.init.body as string)).toEqual({
      mode: 'continuous',
    })
    // A policy write queues a switch job, and the server rejects it without an
    // idempotency key — a 422 the operator sees as "参数无效". The bar missed
    // this while the old per-row control carried it through sourceCommand.
    const headers = new Headers(mutation.init.headers)
    expect(headers.get('Idempotency-Key')).toBeTruthy()
  }
  setup.client.clear()
})

// The event switch is independent of the mode: applying "off + event on" writes
// both, because they are different settings on the same row.
test('applying the event switch is independent of the mode', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('选择 CH01'))
  await userEvent.click(view.getByRole('button', { name: '事件录像开关' }))
  await userEvent.click(view.getByRole('button', { name: /应用到所选通道/ }))
  await expect
    .poll(
      () =>
        setup.mutations.filter((m) => m.path.endsWith('/event-recording'))
          .length
    )
    .toBe(1)
  const write = setup.mutations.find((m) => m.path.endsWith('/event-recording'))
  expect(JSON.parse(write?.init.body as string)).toEqual({ enabled: true })
  // The mode did not change, so no policy write was made.
  expect(
    setup.mutations.filter((m) => m.path.endsWith('/recording-policy')).length
  ).toBe(0)
  setup.client.clear()
})

// Picking 定时录像 opens the schedule editor inside the bar, not in a table row.
test('picking planned expands the schedule editor in the bar', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('button', { name: '定时录像' }))
  await expect.element(view.getByText(/编辑周计划时段|整周一次保存/)).toBeVisible()
  setup.client.clear()
})

// A running manual window is the one state that is not a setting, so it appears
// as a badge on its own rather than as a control.
test('a running manual window shows as a badge in the row', async () => {
  const setup = fixture({ manual: true })
  const view = await render(setup.view)
  await expect.element(view.getByTestId('manual-recording-active')).toBeVisible()
  setup.client.clear()
})

// A scheduled channel shows its week as pills plus a summary; the badge is the
// answer to "when does this record", which the mode badge alone cannot give.
test('a scheduled channel shows its week and time summary', async () => {
  const setup = fixture({
    mode: 'planned',
    schedule: [{ weekday: 1, start_minute: 480, end_minute: 1140 }],
  })
  const view = await render(setup.view)
  const row = view.getByTestId('policy-row')
  await expect.element(row.getByText('定时录像')).toBeVisible()
  await expect.element(row.getByText('08:00 – 19:00')).toBeVisible()
  setup.client.clear()
})

// The event badge states the window it keeps, because "事件录像 · 开" alone does
// not say whether footage before the detection survives.
test('the event badge states the pre and post window', async () => {
  const setup = fixture({ eventOn: true })
  const view = await render(setup.view)
  const badge = view.getByTestId('event-recording-badge')
  await expect.element(badge).toBeVisible()
  await expect.element(badge.getByText(/前5s\/后5s/)).toBeVisible()
  setup.client.clear()
})

// Select-all and clear are the shortcuts for the most repeated action here.
test('select all and clear move the whole selection', async () => {
  const setup = fixture({ peer: true })
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('button', { name: '全选' }))
  await expect.element(view.getByText(/已选 2 \/ 2 路/)).toBeVisible()
  await userEvent.click(view.getByRole('button', { name: '清空' }))
  await expect.element(view.getByText(/已选 0 \/ 2 路/)).toBeVisible()
  setup.client.clear()
})

// Lead and trail are a site setting, shown on the event switch because that is
// where the operator forms an expectation of what a clip contains. The control
// must open, state the ranges, and save through the site endpoint.
test('event clip lead and trail are editable and say they are site-wide', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('button', { name: '调整时长' }))
  await expect.element(view.getByLabelText('事件录像前置秒数')).toBeVisible()
  await expect.element(view.getByLabelText('事件录像后置秒数')).toBeVisible()
  // A control scoped to "all channels" while sitting in a per-channel bar has to
  // say so, or it reads as belonging to the selection.
  await expect.element(view.getByText(/全站设置，所有通道共用/)).toBeVisible()
  // And it must say when the change lands: an open recording keeps its old close.
  await expect.element(view.getByText(/从下一个事件起生效/)).toBeVisible()
})
