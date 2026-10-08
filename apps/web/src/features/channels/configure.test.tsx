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
function fixture(
  options: { testFails?: boolean; stale?: boolean; continuous?: boolean } = {}
) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  let saved = 0,
    applied = false
  const mutations: { path: string; init: RequestInit }[] = []
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method === 'POST') {
      mutations.push({ path, init })
      if (path.endsWith('/source-revisions')) {
        saved++
        return revision
      }
      if (path.endsWith('/test')) {
        if (options.testFails) throw new Error('连接测试暂时无法提交')
        return { job_id: 'test-job', test_id: 'test-proof', state: 'queued' }
      }
      if (path.endsWith('/source/apply')) {
        applied = true
        return { job_id: 'apply-job', state: 'queued' }
      }
      throw new Error('Unexpected mutation ' + path)
    }
    if (path.startsWith('/api/v1/channels?'))
      return { items: [channel], next_cursor: null }
    if (path.endsWith('/source/status'))
      return {
        ...status,
        version: 1 + saved + (options.stale && saved ? 1 : 0),
        current_revision_id: applied || options.continuous ? revision.id : null,
        requires_initial_recording_mode: !options.continuous,
        storage_pool_id: options.continuous ? 'pool-1' : null,
      }
    if (path.includes('/source-revisions?'))
      return {
        items: saved || options.continuous ? [revision] : [],
        next_cursor: null,
      }
    if (path.endsWith('/source-tests/test-proof'))
      return {
        id: 'test-proof',
        revision_id: revision.id,
        state: 'succeeded',
        main: { state: 'healthy', first_frame: true },
        sub: { state: 'healthy', first_frame: true },
        observed_at: new Date().toISOString(),
        expires_at: new Date(Date.now() + 120000).toISOString(),
      }
    if (path.startsWith('/api/v1/jobs/'))
      return { id: path.split('/').pop(), state: 'succeeded' }
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
async function fill(view: Awaited<ReturnType<typeof render>>) {
  await userEvent.fill(
    view.getByLabelText('IP 地址', { exact: true }),
    '192.168.33.20'
  )
  await userEvent.fill(
    view.getByLabelText('密码', { exact: true }),
    'isolated-fixture-password'
  )
}
test('one click tests and connects a first camera without requiring storage or recording', async () => {
  const setup = fixture(),
    view = await render(setup.view)
  await fill(view)
  await userEvent.click(
    view.getByRole('button', { name: '保存并启用', exact: true })
  )
  await expect
    .element(view.getByText('摄像头已连接', { exact: true }))
    .toBeVisible()
  const apply = setup.mutations.find((m) => m.path.endsWith('/source/apply'))!
  expect(JSON.parse(apply.init.body as string)).toEqual({
    revision_id: revision.id,
    test_id: 'test-proof',
    first_recording_mode: 'none',
  })
  expect(new Headers(apply.init.headers).get('If-Match')).toBe('"2"')
  expect(
    setup.mutations.filter((m) => m.path.endsWith('/source/apply'))
  ).toHaveLength(1)
  setup.client.clear()
})
test('failed connection retains the first form credentials and retries as add, not modify', async () => {
  const setup = fixture({ testFails: true }),
    view = await render(setup.view)
  await fill(view)
  await userEvent.click(
    view.getByRole('button', { name: '保存并启用', exact: true })
  )
  await expect
    .element(view.getByRole('alert'))
    .toHaveTextContent('连接测试暂时无法提交')
  await expect
    .element(view.getByLabelText('密码', { exact: true }))
    .toHaveValue('isolated-fixture-password')
  await userEvent.click(
    view.getByRole('button', { name: '保存并启用', exact: true })
  )
  await expect
    .poll(
      () =>
        setup.mutations.filter((m) => m.path.endsWith('/source-revisions'))
          .length
    )
    .toBe(2)
  const drafts = setup.mutations.filter((m) =>
    m.path.endsWith('/source-revisions')
  )
  expect(JSON.parse(drafts[1].init.body as string)).toMatchObject({
    identity_intent: 'replace',
    credentials: {
      password_action: 'replace',
      password: 'isolated-fixture-password',
    },
  })
  expect(setup.mutations.some((m) => m.path.endsWith('/source/apply'))).toBe(
    false
  )
  setup.client.clear()
})
test('does not overwrite a concurrently changed channel after connection test', async () => {
  const setup = fixture({ stale: true }),
    view = await render(setup.view)
  await fill(view)
  await userEvent.click(
    view.getByRole('button', { name: '保存并启用', exact: true })
  )
  await expect
    .element(view.getByRole('alert'))
    .toHaveTextContent('通道配置已改变')
  expect(setup.mutations.some((m) => m.path.endsWith('/source/apply'))).toBe(
    false
  )
  setup.client.clear()
})
test('connecting never inspects or changes recording, which lives on the recording plan page', async () => {
  const setup = fixture({ continuous: true }),
    view = await render(setup.view)
  await userEvent.click(
    view.getByRole('button', { name: '保存并启用', exact: true })
  )
  await expect
    .element(view.getByText('摄像头已连接', { exact: true }))
    .toBeVisible()
  // The pool is a recording precondition, so bringing a camera up must not
  // probe it, and an already-running policy must be left exactly as it was.
  expect(
    setup.mutations.some((m) => m.path.includes('/storage-pools/'))
  ).toBe(false)
  expect(
    setup.mutations.some((m) => m.path.endsWith('/recording-policy'))
  ).toBe(false)
  const apply = setup.mutations.find((m) => m.path.endsWith('/source/apply'))!
  expect(JSON.parse(apply.init.body as string)).not.toHaveProperty(
    'first_recording_mode'
  )
  setup.client.clear()
})

test('saving alone records the camera and neither tests nor switches the source', async () => {
  const setup = fixture(),
    view = await render(setup.view)
  await fill(view)
  await userEvent.click(view.getByRole('button', { name: '保存', exact: true }))
  await expect
    .element(view.getByText('已保存，未启用；启用请点「保存并启用」', { exact: true }))
    .toBeVisible()
  expect(
    setup.mutations.filter((m) => m.path.endsWith('/source-revisions'))
  ).toHaveLength(1)
  expect(setup.mutations.some((m) => m.path.endsWith('/test'))).toBe(false)
  expect(setup.mutations.some((m) => m.path.endsWith('/source/apply'))).toBe(
    false
  )
  setup.client.clear()
})
