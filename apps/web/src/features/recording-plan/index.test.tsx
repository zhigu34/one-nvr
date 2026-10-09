import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
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
}
function fixture(options: { pool?: boolean; reject?: boolean } = {}) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  const mutations: { path: string; init: RequestInit }[] = []
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method === 'PUT' && path.endsWith('/recording-policies')) {
      mutations.push({ path, init })
      return {
        items: options.reject
          ? [
              {
                channel_id: channel.id,
                state: 'rejected',
                error_code: 'zlm_write_evidence_unavailable',
              },
            ]
          : [{ channel_id: channel.id, state: 'queued', job_id: 'job-1' }],
      }
    }
    if (path.startsWith('/api/v1/channels?'))
      return { items: [channel], next_cursor: null }
    if (path.endsWith('/recording-policy'))
      return {
        channel_id: channel.id,
        version: 4,
        mode: 'none',
        event_recording_enabled: false,
      }
    if (path.endsWith('/source/status'))
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
    if (path.startsWith('/api/v1/storage-pools'))
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
test('a batch reads each current version, applies in one request and reports rejections', async () => {
  const setup = fixture({ pool: true, reject: true })
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
    .poll(() => setup.mutations.length)
    .toBe(1)
  expect(JSON.parse(setup.mutations[0].init.body as string)).toEqual({
    items: [
      {
        channel_id: channel.id,
        expected_version: 4,
        mode: 'continuous',
      },
    ],
  })
  // A rejected row must surface its own public reason instead of a silent success.
  await expect
    .element(view.getByText(/zlm_write_evidence_unavailable/))
    .toBeVisible()
})
