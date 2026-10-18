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
const healthyCheck: Schema<'PoolCheck'> = {
  service: 'zlm',
  state: 'healthy',
  reason: 'zlm_media_verified',
  total_bytes: 0,
  free_bytes: 0,
  filesystem_id: '',
}
const awaitingSource: Schema<'PoolCheck'> = {
  service: 'zlm',
  state: 'pending',
  reason: 'test_source_required',
  total_bytes: 0,
  free_bytes: 0,
  filesystem_id: '',
}
function fixture(
  options: {
    pool?: boolean
    reject?: boolean
    bindReject?: boolean
    checks?: Schema<'PoolCheck'>[]
    manual?: 'idle' | 'running' | 'denied'
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
  const attempts = { batch: 0, bind: 0 }
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
    if (method === 'PUT' && path.endsWith('/storage-pool')) {
      attempts.bind++
      if (options.bindReject && attempts.bind === 1) {
        throw new ApiError(
          409,
          'zlm_write_evidence_unavailable',
          '存储池检查未通过，请先运行存储池检查'
        )
      }
      return { job_id: 'job-bind', state: 'queued' }
    }
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
            recording_mode: 'none',
            source_ip: '192.168.66.114',
            main: { state: 'healthy', reason: '', observed_at: null, expires_at: null },
            sub: { state: 'unknown', reason: 'observation_missing', observed_at: null, expires_at: null },
            recording: { state: 'disabled', reason: 'recording_disabled', observed_at: null, expires_at: null },
          },
        ],
      }
    if (method === 'GET' && path.startsWith('/api/v1/channels?'))
      return { items: [channel], next_cursor: null }
    // The manual window probe answers 404 when nothing is running; the control
    // must read that as "idle" and only surface a warning for real failures.
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
test('unsupported recording modes are listed but cannot be chosen', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  const select = view.getByLabelText('CH01 录像方式')
  await expect.element(select).toBeVisible()
  await userEvent.click(select)
  await expect
    .element(
      view.getByRole('option', { name: '定时录像（后续开放）', exact: true })
    )
    .toHaveAttribute('data-disabled')
  await expect
    .element(
      view.getByRole('option', { name: '事件录像（后续开放）', exact: true })
    )
    .toHaveAttribute('data-disabled')
  await expect
    .element(
      view.getByRole('option', { name: '手动（连续录像）', exact: true })
    )
    .not.toHaveAttribute('data-disabled')
})
test('continuous recording without a bound pool is refused before it reaches the API', async () => {
  const setup = fixture({ pool: false })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('CH01 录像方式'))
  await userEvent.click(
    view.getByRole('option', { name: '手动（连续录像）', exact: true })
  )
  await expect.element(view.getByText('请先绑定存储池')).toBeVisible()
  await expect
    .element(view.getByRole('button', { name: '应用', exact: true }))
    .toBeDisabled()
})
test('binding recovers from an expired write proof by re-verifying and retrying', async () => {
  const setup = fixture({ bindReject: true, checks: [healthyCheck] })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('CH01 存储池'))
  await userEvent.click(view.getByRole('option', { name: 'disk1', exact: true }))
  const bind = view.getByRole('button', { name: '绑定', exact: true })
  await expect.element(bind).toBeEnabled()
  await userEvent.click(bind)
  // The rejected attempt is invisible to the operator: a real check runs and
  // the bind is retried within the same action.
  await expect
    .poll(() => setup.mutations.filter((m) => m.path.endsWith('/storage-pool')).length)
    .toBe(2)
  expect(
    setup.mutations.some(
      (m) => m.path.endsWith('/test') && m.init.method === 'POST'
    )
  ).toBe(true)
  // Settlement reads as the inline ✓ badge; a bind shows the bound variant.
  await expect.element(view.getByText('✓ 已绑定')).toBeVisible()
})
test('a batch recovers rows rejected for a stale write proof', async () => {
  const setup = fixture({ pool: true, reject: true, checks: [healthyCheck] })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('选择 CH01'))
  await userEvent.click(view.getByLabelText('批量录像方式'))
  await userEvent.click(
    view.getByRole('option', { name: '手动（连续录像）', exact: true })
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
test('a batch reports the pool reason when recovery cannot verify the write', async () => {
  const setup = fixture({ pool: true, reject: true, checks: [awaitingSource] })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('选择 CH01'))
  await userEvent.click(view.getByLabelText('批量录像方式'))
  await userEvent.click(
    view.getByRole('option', { name: '手动（连续录像）', exact: true })
  )
  await userEvent.click(
    view.getByRole('button', { name: '批量应用', exact: true })
  )
  await expect
    .element(view.getByText(/需要先完成一路摄像头的连接测试或启用/))
    .toBeVisible()
  // No retry without fresh evidence: the gate was not bypassed.
  expect(
    setup.mutations.filter((m) => m.path.endsWith('/recording-policies')).length
  ).toBe(1)
})

// Dirty-state gating: untouched rows carry dimmed buttons instead of thirty-two
// standing invites; only a real edit lights the action up.
test('untouched rows keep dimmed actions that light up on edit', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  const bind = view.getByRole('button', { name: '绑定', exact: true })
  await expect.element(bind).toBeDisabled()
  const apply = view.getByRole('button', { name: '应用', exact: true })
  await expect.element(apply).toBeDisabled()
  // No pool is bound in this fixture: continuous stays blocked by the
  // forgotten-bind guard even after the mode edit.
  await userEvent.click(view.getByLabelText('CH01 录像方式'))
  await userEvent.click(
    view.getByRole('option', { name: '手动（连续录像）', exact: true })
  )
  await expect.element(apply).toBeDisabled()
  await userEvent.click(view.getByLabelText('CH01 存储池'))
  await userEvent.click(view.getByRole('option', { name: 'disk1', exact: true }))
  await expect.element(bind).toBeEnabled()
  // With the pool drafted, the chain clears the guard and apply lights up.
  await expect.element(apply).toBeEnabled()
  setup.client.clear()
})

// Applying a mode on a row with an unsaved pool edit chains the bind first, so
// enabling recording cannot fail on a forgotten bind.
test('applying a mode chains the unsaved pool bind before the policy', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('CH01 存储池'))
  await userEvent.click(view.getByRole('option', { name: 'disk1', exact: true }))
  await userEvent.click(view.getByLabelText('CH01 录像方式'))
  await userEvent.click(
    view.getByRole('option', { name: '手动（连续录像）', exact: true })
  )
  await userEvent.click(
    view.getByRole('button', { name: '应用', exact: true })
  )
  // One click, two writes in order: the pool bind, then the policy.
  await expect
    .poll(
      () =>
        setup.mutations.filter((m) => m.path.endsWith('/storage-pool')).length
    )
    .toBe(1)
  await expect
    .poll(
      () =>
        setup.mutations.filter((m) => m.path.endsWith('/recording-policy'))
          .length
    )
    .toBe(1)
  const poolIndex = setup.mutations.findIndex((m) =>
    m.path.endsWith('/storage-pool')
  )
  const policyIndex = setup.mutations.findIndex((m) =>
    m.path.endsWith('/recording-policy')
  )
  expect(poolIndex).toBeLessThan(policyIndex)
  expect(JSON.parse(setup.mutations[poolIndex].init.body as string)).toEqual({
    pool_id: '00000000-0000-4000-8000-000000000020',
  })
  // Settlement reads as the inline ✓ badge on the action that ran last.
  await expect
    .poll(
      () =>
        (document.querySelector('[data-testid="policy-row"]')?.textContent || '').includes(
          '已生效'
        )
    )
    .toBe(true)
  setup.client.clear()
})

// The bound pool links to the storage page instead of carrying a second
// standing button, and the row reads the channel's live state.
test('the bound pool shows a storage-page link and the row shows its health', async () => {
  const setup = fixture({ pool: true })
  const view = await render(setup.view)
  await expect.element(view.getByText('大门')).toBeVisible()
  // healthy main + recording disabled → 在线; the recording-disabled pill on
  // the status payload drives it.
  await expect.element(view.getByText('在线')).toBeVisible()
  const link = view.getByRole('link', { name: 'CH01 检查存储池' })
  await expect
    .element(link)
    .toHaveAttribute('href', '/storage?tab=pools')
  setup.client.clear()
})

// REC-03: a manual window is a separate control from the policy select, because
// REC-04 requires it to survive a policy edit. The row shows the countdown when
// one is running and offers the durations when none is.
test('a running manual window shows its remaining time', async () => {
  const setup = fixture({ manual: 'running' })
  const view = await render(setup.view)
  await expect.element(view.getByTestId('manual-recording-active')).toBeVisible()
  await expect.element(view.getByText(/手动录像 25 分钟/)).toBeVisible()
  await expect
    .element(view.getByRole('button', { name: 'CH01 停止手动录像' }))
    .toBeVisible()
  setup.client.clear()
})

test('starting a manual window posts the chosen duration', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('button', { name: 'CH01 手动录像' }))
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
