import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
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
    if (method === 'GET' && path.startsWith('/api/v1/channels?'))
      return { items: [channel], next_cursor: null }
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
  return {
    mutations,
    view: (
      <QueryClientProvider
        client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
      >
        <RecordingPlan />
      </QueryClientProvider>
    ),
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
  await expect.element(view.getByText('已完成')).toBeVisible()
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
    view.getByRole('button', { name: '应用到选中通道', exact: true })
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
    view.getByRole('button', { name: '应用到选中通道', exact: true })
  )
  await expect
    .element(view.getByText(/需要先完成一路摄像头的连接测试或启用/))
    .toBeVisible()
  // No retry without fresh evidence: the gate was not bypassed.
  expect(
    setup.mutations.filter((m) => m.path.endsWith('/recording-policies')).length
  ).toBe(1)
})
