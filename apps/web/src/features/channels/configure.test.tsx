import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import type { Schema } from '@/lib/types'
import { ChannelConfigure } from './configure'

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
  version: 1,
  permissions: ['configure', 'playback'],
}
const observation: Schema<'ObservedStatus'> = {
  state: 'not_configured',
  reason: 'not_configured',
  observed_at: null,
  expires_at: null,
}
const status: Schema<'ChannelSourceStatus'> = {
  channel_id: channel.id,
  current_revision_id: null,
  desired_revision_id: null,
  storage_pool_id: null,
  enabled: true,
  version: 1,
  main: observation,
  sub: observation,
  recording: { ...observation, state: 'disabled' },
  requires_initial_recording_mode: true,
}
const revision: Schema<'SourceRevision'> = {
  id: '00000000-0000-4000-8000-000000000010',
  channel_id: channel.id,
  source_id: '00000000-0000-4000-8000-000000000011',
  number: 1,
  config: {
    ip: '192.168.33.20',
    rtsp_port: 554,
    main_path: '/main',
    sub_path: '/sub',
    transport: 'tcp',
  },
  username_summary: 'a***',
  password_state: 'saved',
  created_at: '2026-10-07T00:00:00Z',
}
function fixture(testFails = false) {
  useAuthStore
    .getState()
    .setUser({
      id: 'admin',
      username: 'admin',
      role: 'admin',
      enabled: true,
      version: 1,
    })
  let saved = false
  const mutations: string[] = []
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method === 'POST') {
      mutations.push(path)
      if (path.endsWith('/source-revisions')) {
        saved = true
        return revision
      }
      if (path.endsWith('/test')) {
        if (testFails) throw new Error('连接测试暂时无法提交')
        return { job_id: 'test-job', test_id: 'test-proof', state: 'queued' }
      }
      throw new Error('Unexpected mutation ' + path)
    }
    if (path.startsWith('/api/v1/channels?'))
      return { items: [channel], next_cursor: null }
    if (path.endsWith('/source/status')) return status
    if (path.includes('/source-revisions?'))
      return { items: saved ? [revision] : [], next_cursor: null }
    if (path.endsWith('/recording-policy'))
      return {
        channel_id: channel.id,
        version: 1,
        mode: 'none',
        event_recording_enabled: false,
      }
    if (path.startsWith('/api/v1/storage-pools'))
      return { items: [], next_cursor: null }
    if (path.endsWith('/source-tests/test-proof'))
      return {
        id: 'test-proof',
        revision_id: revision.id,
        state: 'queued',
        main: { state: 'unknown', first_frame: false },
        sub: { state: 'unknown', first_frame: false },
        observed_at: null,
        expires_at: null,
      }
    throw new Error('Unexpected query ' + path)
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return {
    client,
    mutations,
    view: (
      <QueryClientProvider client={client}>
        <ChannelConfigure initialId={channel.id} />
      </QueryClientProvider>
    ),
  }
}

test('saving a connection queues its test without applying the source', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.fill(
    view.getByLabelText('IP 地址', { exact: true }),
    '192.168.33.20'
  )
  await userEvent.fill(
    view.getByLabelText('新密码', { exact: true }),
    'isolated-fixture-password'
  )
  await userEvent.click(
    view.getByRole('button', { name: '保存并测试', exact: true })
  )
  await expect
    .poll(() => setup.mutations)
    .toEqual([
      `/api/v1/channels/${channel.id}/source-revisions`,
      `/api/v1/channels/${channel.id}/source-revisions/${revision.id}/test`,
    ])
  await expect
    .element(view.getByRole('button', { name: '应用配置', exact: true }))
    .toBeDisabled()
  expect(setup.mutations.some((path) => path.endsWith('/source/apply'))).toBe(
    false
  )
  setup.client.clear()
})

test('a failed test submission keeps the saved draft available for retry', async () => {
  const setup = fixture(true)
  const view = await render(setup.view)
  await userEvent.fill(
    view.getByLabelText('IP 地址', { exact: true }),
    '192.168.33.20'
  )
  await userEvent.fill(
    view.getByLabelText('新密码', { exact: true }),
    'isolated-fixture-password'
  )
  await userEvent.click(
    view.getByRole('button', { name: '保存并测试', exact: true })
  )
  await expect
    .element(view.getByText('连接测试暂时无法提交', { exact: false }))
    .toBeVisible()
  await expect
    .element(view.getByRole('button', { name: '测试取流', exact: true }))
    .toBeEnabled()
  await expect
    .element(view.getByLabelText('IP 地址', { exact: true }))
    .toHaveValue('192.168.33.20')
  expect(setup.mutations).toHaveLength(2)
  setup.client.clear()
})
