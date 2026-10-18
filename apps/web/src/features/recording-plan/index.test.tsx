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
import { ApiError } from '@/lib/api-client'
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
}
const peer: Schema<'Channel'> = {
  ...channel,
  id: '00000000-0000-4000-8000-000000000002',
  channel_no: 2,
  channel_name: '后门',
}
const healthyCheck: Schema<'PoolCheck'> = {
  service: 'zlm',
  state: 'healthy',
  reason: 'zlm_media_verified',
  total_bytes: 0,
  free_bytes: 0,
  filesystem_id: '',
}
function fixture(
  options: {
    pool?: boolean
    reject?: boolean
    checks?: Schema<'PoolCheck'>[]
    manual?: 'idle' | 'running' | 'denied'
    // A second channel, so "copy to other channels" has a real target.
    peer?: boolean
    // An unconfigured channel, so the collapsed row group has a member.
    unconfigured?: boolean
    // A stored planned mode, so the mini week renders from the server answer.
    planned?: { windows: { weekday: number; start_minute: number; end_minute: number }[] }
  } = {}
) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  const pool: Schema<'Pool'> = {
    id: '00000000-0000-4000-8000-000000000020',
    name: 'disk1',
    path: '/storage/disk1',
    enabled: true,
    is_default: true,
    version: 1,
    checks: options.checks || [],
    state: 'ready',
    used_bytes: null,
    warning_percent: 80,
    pressure_percent: 85,
    pressure_target_percent: 80,
    critical_percent: 95,
  }
  const mutations: { path: string; init: RequestInit }[] = []
  const attempts = { batch: 0 }
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    const method = init?.method && init.method !== 'GET' ? init.method : 'GET'
    if (method !== 'GET') mutations.push({ path, init: init as RequestInit })
    if (method === 'PUT' && path.endsWith('/recording-policies')) {
      attempts.batch++
      if (options.reject && attempts.batch === 1) {
        return {
          items: [
            {
              channel_id: channel.id,
              state: 'rejected',
              error_code: 'zlm_write_evidence_unavailable',
            },
          ],
        }
      }
      return {
        items: [{ channel_id: channel.id, state: 'queued', job_id: 'job-1' }],
      }
    }
    if (method === 'PUT' && path.endsWith('/recording-policy'))
      return { job_id: 'job-policy', state: 'queued' }
    if (method === 'POST' && path.endsWith('/test'))
      return { job_id: 'job-check' }
    if (method === 'GET' && path.startsWith('/api/v1/jobs/'))
      return {
        id: 'job-1',
        kind: 'storage.check',
        state: 'succeeded',
        attempts: 1,
      }
    if (method === 'GET' && path === '/api/v1/channels/summary')
      return {
        items: [
          {
            channel_id: channel.id,
            channel_no: 1,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 4,
            permissions: ['configure'],
            current_revision_id: 'rev',
            storage_pool_id: options.pool ? pool.id : null,
            recording_mode: options.planned ? 'planned' : 'none',
            source_ip: '192.168.66.114',
            main: { state: 'healthy', reason: '', observed_at: null, expires_at: null },
            sub: { state: 'unknown', reason: 'observation_missing', observed_at: null, expires_at: null },
            recording: { state: 'disabled', reason: 'recording_disabled', observed_at: null, expires_at: null },
          },
          ...(options.unconfigured
            ? [
                {
                  channel_id: '00000000-0000-4000-8000-000000000009',
                  channel_no: 9,
                  channel_name: '通道 09',
                  channel_group: '',
                  enabled: true,
                  version: 1,
                  permissions: ['configure'],
                  current_revision_id: null,
                  storage_pool_id: null,
                  recording_mode: 'none',
                  source_ip: '',
                  main: { state: 'not_configured', reason: 'source_not_configured', observed_at: null, expires_at: null },
                  sub: { state: 'not_configured', reason: 'source_not_configured', observed_at: null, expires_at: null },
                  recording: { state: 'disabled', reason: 'source_not_configured', observed_at: null, expires_at: null },
                },
              ]
            : []),
          // The peer must appear in the summary too: schedulePeers (the copy
          // targets) come from configured channels, and configuration is judged
          // from the summary, not the channel list alone.
          ...(options.peer
            ? [
                {
                  channel_id: peer.id,
                  channel_no: 2,
                  channel_name: '后门',
                  channel_group: '',
                  enabled: true,
                  version: 4,
                  permissions: ['configure'],
                  current_revision_id: 'rev',
                  storage_pool_id: options.pool ? pool.id : null,
                  recording_mode: 'none',
                  source_ip: '192.168.66.115',
                  main: { state: 'healthy', reason: '', observed_at: null, expires_at: null },
                  sub: { state: 'unknown', reason: 'observation_missing', observed_at: null, expires_at: null },
                  recording: { state: 'disabled', reason: 'recording_disabled', observed_at: null, expires_at: null },
                },
              ]
            : []),
        ],
      }
    if (method === 'GET' && path.startsWith('/api/v1/channels?'))
      return {
        items: options.unconfigured
          ? [channel, { ...channel, id: '00000000-0000-4000-8000-000000000009', channel_no: 9, channel_name: '通道 09' }]
          : options.peer
            ? [channel, peer]
            : [channel],
        next_cursor: null,
      }
    // The manual window probe answers 404 when nothing is running; the control
    // must read that as "idle" and only surface a warning for real failures.
    // Weekly schedule (REC-02). The editor is only rendered for planned
    // channels, so this answers for the mode-change cases as well.
    if (method === 'GET' && path.endsWith('/recording-schedule')) {
      return {
        channel_id: channel.id,
        windows: options.planned?.windows || [],
        timezone: 'Asia/Shanghai',
        active: false,
      }
    }
    if (method === 'PUT' && path.endsWith('/recording-schedule')) {
      const body = JSON.parse(init?.body as string) as {
        windows: { weekday: number; start_minute: number; end_minute: number }[]
      }
      return {
        channel_id: channel.id,
        windows: body.windows,
        timezone: 'Asia/Shanghai',
        active: false,
      }
    }
    if (method === 'POST' && path.endsWith('/manual-recording')) {
      return {
        channel_id: channel.id,
        requested_minutes: 30,
        started_at: new Date().toISOString(),
        expires_at: new Date(Date.now() + 30 * 60_000).toISOString(),
        remaining_ms: 30 * 60_000,
      }
    }
    if (method === 'DELETE' && path.endsWith('/manual-recording')) return {}
    if (method === 'GET' && path.endsWith('/manual-recording')) {
      if (options.manual === 'denied')
        throw new ApiError(500, 'service_error', '服务暂时不可用')
      if (options.manual !== 'running')
        throw new ApiError(404, 'manual_recording_absent', '该通道当前没有手动录像')
      return {
        channel_id: channel.id,
        requested_minutes: 30,
        started_at: new Date(Date.now() - 5 * 60_000).toISOString(),
        expires_at: new Date(Date.now() + 25 * 60_000).toISOString(),
        remaining_ms: 25 * 60_000,
      }
    }
    if (method === 'GET' && path.endsWith('/recording-policy'))
      return {
        channel_id: channel.id,
        version: 4,
        mode: 'none',
        event_recording_enabled: false,
      }
    if (method === 'GET' && path.endsWith('/source/status'))
      return {
        channel_id: channel.id,
        current_revision_id: 'rev',
        desired_revision_id: 'rev',
        storage_pool_id: options.pool ? pool.id : null,
        enabled: true,
        version: 4,
        main: { state: 'healthy', reason: '' },
        sub: { state: 'unhealthy', reason: '' },
        recording: { state: 'disabled', reason: 'recording_disabled' },
        requires_initial_recording_mode: false,
      }
    if (method === 'GET' && path.startsWith('/api/v1/storage-pools'))
      return { items: [pool], next_cursor: null }
    throw new Error('Unexpected query ' + path)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
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
  return {
    mutations,
    client,
    view: <RouterProvider router={router} />,
  }
}

test('modes keep the product vocabulary and event recording stays a placeholder', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  const select = view.getByLabelText('CH01 录像方式')
  await expect.element(select).toBeVisible()
  await userEvent.click(select)
  await expect
    .element(view.getByRole('option', { name: '全天录像', exact: true }))
    .not.toHaveAttribute('data-disabled')
  await expect
    .element(
      view.getByRole('option', { name: '事件录像（后续开放）', exact: true })
    )
    .toHaveAttribute('data-disabled')
  await expect
    .element(view.getByRole('option', { name: '定时录像', exact: true }))
    .not.toHaveAttribute('data-disabled')
})

// Binding moved to the storage page: the pool column is read-only there, so a
// bound channel shows the pool name and an unbound one links over to /storage.
test('the pool column is read-only with a link to storage for unbound rows', async () => {
  const setup = fixture({ pool: false })
  const view = await render(setup.view)
  const link = view.getByRole('link', { name: /未绑定 · 去分配/ })
  await expect.element(link).toBeVisible()
  await expect
    .element(link)
    .toHaveAttribute('href', '/storage?tab=pools')
  // No per-row pool select and no bind button: the controls that moved away.
  expect(view.getByLabelText('CH01 存储池').query()).toBeNull()
  expect(
    view.getByRole('button', { name: '绑定', exact: true }).query()
  ).toBeNull()
  setup.client.clear()
})

test('a bound row shows the pool as a static pill with a storage link', async () => {
  const setup = fixture({ pool: true })
  const view = await render(setup.view)
  await expect.element(view.getByText('disk1')).toBeVisible()
  const link = view.getByRole('link', { name: /前往存储页管理 disk1/ })
  await expect
    .element(link)
    .toHaveAttribute('href', '/storage?tab=pools')
  setup.client.clear()
})

// none/continuous apply straight from the select: a visible flip that still
// needs a second confirm click reads as two controls fighting over one value.
test('switching to continuous applies immediately without an apply button', async () => {
  const setup = fixture({ pool: true })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('CH01 录像方式'))
  await userEvent.click(
    view.getByRole('option', { name: '全天录像', exact: true })
  )
  await expect
    .poll(
      () =>
        setup.mutations.filter((m) => m.path.endsWith('/recording-policy'))
          .length
    )
    .toBe(1)
  const put = setup.mutations.find((m) => m.path.endsWith('/recording-policy'))
  expect(JSON.parse(put?.init.body as string)).toEqual({ mode: 'continuous' })
  // The row shows the settlement badge; there is no 应用 button anywhere.
  await expect.element(view.getByTestId('applied-policy')).toBeVisible()
  expect(
    view.getByRole('button', { name: '应用', exact: true }).query()
  ).toBeNull()
  setup.client.clear()
})

test('switching to planned opens the editor instead of writing', async () => {
  const setup = fixture({ pool: true })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('CH01 录像方式'))
  await userEvent.click(view.getByRole('option', { name: '定时录像', exact: true }))
  // The editor is open (single save button) and no policy write happened yet:
  // the editor's save is the only path that commits a planned switch.
  await expect
    .element(
      view.getByRole('button', { name: /保存并启用定时录像/ })
    )
    .toBeVisible()
  expect(
    setup.mutations.filter((m) => m.path.endsWith('/recording-policy')).length
  ).toBe(0)
  // While the editor is open, the mini pills hide (the panel shows the same
  // days at full size) and the panel's own close button reads 收起.
  expect(view.getByRole('img', { name: /周计划/ }).query()).toBeNull()
  await expect
    .element(view.getByRole('button', { name: '收起', exact: true }))
    .toBeVisible()
  setup.client.clear()
})

// The mini week renders in the collapsed row for a planned channel: which days
// record is the answer the list owes without expanding anything. The weekday
// names live in the squares; only the time range is spelled out beside them.
test('a planned row shows the weekday mini pills from the stored schedule', async () => {
  const setup = fixture({
    pool: true,
    planned: { windows: [{ weekday: 7, start_minute: 480, end_minute: 540 }] },
  })
  const view = await render(setup.view)
  const pills = view.getByRole('img', { name: /周计划/ })
  await expect.element(pills).toBeVisible()
  // The uniform single range is spelled out beside the pills.
  await expect.element(view.getByText('08:00 – 09:00')).toBeVisible()
  setup.client.clear()
})

// The quick bar applies one range to several days in one gesture; the single
// save button then writes the week and switches the mode together.
test('the quick bar fills several days and one save commits week plus mode', async () => {
  const setup = fixture({ pool: true })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('CH01 录像方式'))
  await userEvent.click(view.getByRole('option', { name: '定时录像', exact: true }))
  await userEvent.click(view.getByRole('button', { name: '选择 周一' }))
  await userEvent.click(view.getByRole('button', { name: '选择 周二' }))
  await userEvent.click(
    view.getByRole('button', { name: /应用到所选星期/ })
  )
  await userEvent.click(
    view.getByRole('button', { name: /保存并启用定时录像/ })
  )
  await expect
    .poll(
      () =>
        setup.mutations.filter(
          (m) => m.init.method === 'PUT' && m.path.endsWith('/recording-schedule')
        ).length
    )
    .toBe(1)
  const schedule = setup.mutations.find(
    (m) => m.init.method === 'PUT' && m.path.endsWith('/recording-schedule')
  )
  const body = JSON.parse(schedule?.init.body as string) as {
    windows: { weekday: number; start_minute: number; end_minute: number }[]
  }
  expect(body.windows).toHaveLength(2)
  expect(new Set(body.windows.map((w) => w.weekday))).toEqual(new Set([1, 2]))
  expect(body.windows.every((w) => w.start_minute === 480 && w.end_minute === 1080)).toBe(true)
  // The mode write follows in the same act.
  await expect
    .poll(
      () =>
        setup.mutations.filter((m) => m.path.endsWith('/recording-policy'))
          .length
    )
    .toBe(1)
  const policy = setup.mutations.find((m) => m.path.endsWith('/recording-policy'))
  expect(JSON.parse(policy?.init.body as string)).toEqual({ mode: 'planned' })
  setup.client.clear()
})

// Filling the whole week is the quick bar's 全周 shortcut: one pick, one apply,
// and the seven rows all carry the range — no per-day copy button to press.
test('the quick bar fills the whole week in one gesture', async () => {
  const setup = fixture({ pool: true })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('CH01 录像方式'))
  await userEvent.click(view.getByRole('option', { name: '定时录像', exact: true }))
  await userEvent.click(view.getByRole('button', { name: '全周' }))
  await userEvent.click(
    view.getByRole('button', { name: /应用到所选星期/ })
  )
  await userEvent.click(
    view.getByRole('button', { name: /保存并启用定时录像/ })
  )
  await expect
    .poll(
      () =>
        setup.mutations.filter(
          (m) => m.init.method === 'PUT' && m.path.endsWith('/recording-schedule')
        ).length
    )
    .toBe(1)
  const put = setup.mutations.find(
    (m) => m.init.method === 'PUT' && m.path.endsWith('/recording-schedule')
  )
  const body = JSON.parse(put?.init.body as string) as {
    windows: { weekday: number }[]
  }
  expect(body.windows).toHaveLength(7)
  expect(new Set(body.windows.map((w) => w.weekday)).size).toBe(7)
  setup.client.clear()
})

// The copy control must appear only when there is somewhere to copy to.
test('the copy-to-channel control is absent without peers', async () => {
  const setup = fixture({ pool: true })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('CH01 录像方式'))
  await userEvent.click(view.getByRole('option', { name: '定时录像', exact: true }))
  await expect
    .element(view.getByText(/复制此时段到其它通道/))
    .not.toBeInTheDocument()
  setup.client.clear()
})

// REC-02's "copy to other channels": the week is written to each selected
// channel, one request each, and the operator sees which ones took it.
test('copying a week writes every selected channel', async () => {
  const setup = fixture({ pool: true, peer: true })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('CH01 录像方式'))
  await userEvent.click(view.getByRole('option', { name: '定时录像', exact: true }))
  // Picking the mode auto-opens the editor; the day rows are already there.
  await userEvent.click(view.getByRole('button', { name: '周一 添加时段' }))
  await userEvent.click(view.getByRole('button', { name: '复制到 CH02' }))
  await userEvent.click(
    view.getByRole('button', { name: '复制到所选通道', exact: true })
  )
  await expect
    .poll(
      () =>
        setup.mutations.filter(
          (m) => m.init.method === 'PUT' && m.path.endsWith('/recording-schedule')
        ).length
    )
    .toBe(1)
  const put = setup.mutations.find(
    (m) => m.init.method === 'PUT' && m.path.endsWith('/recording-schedule')
  )
  // It must target the peer, not the channel being edited.
  expect(put?.path).toContain(peer.id)
  const body = JSON.parse(put?.init.body as string) as {
    windows: { weekday: number }[]
  }
  expect(body.windows).toHaveLength(1)
  await expect.element(view.getByText(/已复制到 CH02/)).toBeVisible()
  setup.client.clear()
})

// A batch recovers rows rejected for a stale write proof by re-running the
// real pool check and retrying just those rows.
test('a batch recovers rows rejected for a stale write proof', async () => {
  const setup = fixture({ pool: true, reject: true, checks: [healthyCheck] })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('选择 CH01'))
  await userEvent.click(view.getByLabelText('批量录像方式'))
  await userEvent.click(
    view.getByRole('option', { name: '全天录像', exact: true })
  )
  await userEvent.click(
    view.getByRole('button', { name: '批量应用', exact: true })
  )
  await expect
    .poll(() => setup.mutations.filter((m) => m.path.endsWith('/recording-policies')).length)
    .toBe(2)
  // The retry re-reads the version and carries the same mode.
  expect(
    JSON.parse(
      setup.mutations.filter((m) => m.path.endsWith('/recording-policies'))[1]
        .init.body as string
    )
  ).toEqual({
    items: [
      { channel_id: channel.id, expected_version: 4, mode: 'continuous' },
    ],
  })
  await expect
    .element(view.getByText(/经自动重新验证存储池写入/))
    .toBeVisible()
})

// Unconfigured rows collapse behind one line: sixteen empty slots must not
// push the rows an operator came for below the fold.
test('unconfigured channels collapse behind one expandable line', async () => {
  const setup = fixture({ unconfigured: true })
  const view = await render(setup.view)
  const toggle = view.getByRole('button', { name: /已隐藏 1 个未配置源通道/ })
  await expect.element(toggle).toBeVisible()
  expect(view.getByText('通道 09').query()).toBeNull()
  await userEvent.click(toggle)
  await expect.element(view.getByText('通道 09')).toBeVisible()
  setup.client.clear()
})

// REC-03: a manual window is a separate control from the policy select, because
// REC-04 requires it to survive a policy edit. The row shows the countdown when
// one is running and offers the durations when none is.
test('a running manual window shows its remaining time', async () => {
  const setup = fixture({ manual: 'running' })
  const view = await render(setup.view)
  await expect.element(view.getByTestId('manual-recording-active')).toBeVisible()
  await expect.element(view.getByText(/限时录像 25 分钟/)).toBeVisible()
  await expect
    .element(view.getByRole('button', { name: 'CH01 停止限时录像' }))
    .toBeVisible()
  setup.client.clear()
})

test('starting a manual window posts the chosen duration', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('button', { name: 'CH01 限时录像' }))
  await userEvent.click(
    view.getByRole('button', { name: 'CH01 录像 10 分钟' })
  )
  await expect
    .poll(() =>
      setup.mutations.filter((m) => m.path.endsWith('/manual-recording')).length
    )
    .toBe(1)
  const post = setup.mutations.find((m) => m.path.endsWith('/manual-recording'))
  expect(post?.init.method).toBe('POST')
  expect(JSON.parse(post?.init.body as string)).toEqual({ minutes: 10 })
  // The policy mode is untouched: the window is not a settings change.
  expect(
    setup.mutations.some((m) => m.path.endsWith('/recording-policy'))
  ).toBe(false)
  setup.client.clear()
})

// A read failure that is not the 404 "nothing running" answer must not be
// rendered as an idle channel, and must not hide the record button — pressing it
// would be a blind write against a channel whose state is unknown.
test('an unreadable manual state is reported instead of assumed idle', async () => {
  const setup = fixture({ manual: 'denied' })
  const view = await render(setup.view)
  await expect.element(view.getByText(/状态未知/)).toBeVisible()
  setup.client.clear()
})

// Batch manual recording: the mode select cannot carry it (start and stop are
// different verbs), so it has its own control. Every selected channel must
// receive its own request, and a selection of two must produce two calls.
test('batch manual recording starts a window on every selected channel', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('选择 CH01'))
  await userEvent.click(
    view.getByRole('button', { name: '开始录像', exact: true })
  )
  await expect
    .poll(
      () =>
        setup.mutations.filter(
          (m) => m.init.method === 'POST' && m.path.endsWith('/manual-recording')
        ).length
    )
    .toBe(1)
  const post = setup.mutations.find(
    (m) => m.init.method === 'POST' && m.path.endsWith('/manual-recording')
  )
  expect(JSON.parse(post?.init.body as string)).toEqual({ minutes: 30 })
  // The stored policy is untouched: a manual window is a request, not a mode.
  expect(setup.mutations.some((m) => m.path.endsWith('/recording-policy'))).toBe(
    false
  )
  await expect.element(view.getByText(/1 路已开始手动录像/)).toBeVisible()
  setup.client.clear()
})

test('batch manual recording stops the selected channels', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('选择 CH01'))
  await userEvent.click(view.getByRole('button', { name: '停止', exact: true }))
  await expect
    .poll(
      () =>
        setup.mutations.filter(
          (m) => m.init.method === 'DELETE' && m.path.endsWith('/manual-recording')
        ).length
    )
    .toBe(1)
  await expect.element(view.getByText(/1 路已停止手动录像/)).toBeVisible()
  setup.client.clear()
})

// The batch buttons require a selection; without one they must not fire a
// request against every channel on the page.
test('batch manual buttons stay disabled with nothing selected', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await expect
    .element(view.getByRole('button', { name: '开始录像', exact: true }))
    .toBeDisabled()
  await expect
    .element(view.getByRole('button', { name: '停止', exact: true }))
    .toBeDisabled()
  expect(setup.mutations).toHaveLength(0)
  setup.client.clear()
})

// A collapsed planned row must be one line: the editor toggle sits in the
// control strip beside 限时录像, and no panel container renders until the panel
// is opened. The previous layout placed the toggle in its own block below the
// controls, which stretched every planned row to two lines even when closed.
test('picking planned opens the panel and collapsing it removes the panel', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('CH01 录像方式'))
  await userEvent.click(view.getByRole('option', { name: '定时录像', exact: true }))
  // Picking planned opens the editor immediately: an editor you still have to
  // find contradicts the single "保存并启用" story.
  await expect.element(view.getByTestId('schedule-panel')).toBeVisible()
  await userEvent.click(view.getByRole('button', { name: 'CH01 收起时段' }))
  // The panel container itself is gone, not merely hidden: that is what keeps
  // the collapsed row at one line.
  await expect
    .element(view.getByTestId('schedule-panel'))
    .not.toBeInTheDocument()
  // And the toggle that opened it lives inside the control strip.
  const controls = await view.getByTestId('policy-controls').element()
  const toggle = await view.getByRole('button', { name: 'CH01 编辑时段' }).element()
  expect(controls.contains(toggle)).toBe(true)
  setup.client.clear()
})
