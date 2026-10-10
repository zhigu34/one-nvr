import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
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
const observation: Schema<'ObservedStatus'> = {
  state: 'not_configured',
  reason: 'not_configured',
  observed_at: null,
  expires_at: null,
}
const healthy: Schema<'ObservedStatus'> = {
  state: 'healthy',
  reason: '',
  observed_at: '2026-10-10T00:00:00Z',
  expires_at: '2026-10-10T00:05:00Z',
}
const first = '00000000-0000-4000-8000-000000000001'
function summary(
  override: Partial<Schema<'ChannelSummary'>> = {}
): Schema<'ChannelSummary'> {
  return {
    channel_id: first,
    channel_no: 1,
    channel_name: '大门',
    channel_group: '一层',
    enabled: true,
    version: 1,
    permissions: ['configure', 'playback'],
    current_revision_id: null,
    main: observation,
    sub: observation,
    recording: { ...observation, state: 'disabled' },
    last_error: '',
    ...override,
  }
}
// The picker groups and labels channels from this list, so the fixture carries
// three of them: two in one group and one disabled and ungrouped.
const channels: Schema<'ChannelSummary'>[] = [
  summary(),
  summary({
    channel_id: '00000000-0000-4000-8000-000000000002',
    channel_no: 2,
    channel_name: '后门',
  }),
  summary({
    channel_id: '00000000-0000-4000-8000-000000000003',
    channel_no: 3,
    channel_name: '车库',
    channel_group: '',
    enabled: false,
  }),
]
const revision: Schema<'SourceRevision'> = {
  id: '00000000-0000-4000-8000-000000000010',
  channel_id: first,
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
    if (path.startsWith('/api/v1/channels/summary'))
      return {
        items: channels.map((channel) =>
          channel.channel_id === first
            ? {
                ...channel,
                version: 1 + saved,
                current_revision_id:
                  applied || options.continuous ? revision.id : null,
                main: applied || options.continuous ? healthy : observation,
              }
            : channel
        ),
      }
    if (path.endsWith('/recording-policy'))
      return {
        channel_id: first,
        mode: options.continuous ? 'continuous' : 'none',
        event_recording_enabled: false,
        version: 1,
      }
    if (path.endsWith('/source/status'))
      return {
        channel_id: first,
        current_revision_id:
          applied || options.continuous ? revision.id : null,
        desired_revision_id: null,
        storage_pool_id: options.continuous ? 'pool-1' : null,
        enabled: true,
        version: 1 + saved + (options.stale && saved ? 1 : 0),
        main: observation,
        sub: observation,
        recording: { ...observation, state: 'disabled' },
        requires_initial_recording_mode: !options.continuous,
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
        <ChannelConfigure initialId={first} />
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
  await userEvent.click(
    view.getByRole('button', { name: '仅保存', exact: true })
  )
  await expect
    .element(
      view.getByText('已保存，未启用；启用请点「保存并启用」', { exact: true })
    )
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

test('the picker groups channels, keeps the edited one open and marks a disabled channel', async () => {
  const setup = fixture(),
    view = await render(setup.view)
  // The grouped list is the wide-screen picker; narrow screens get the select.
  await page.viewport(1280, 900)
  const nav = view.getByRole('navigation', { name: '切换通道' })
  await expect.element(nav.getByText('一层', { exact: true })).toBeVisible()
  await expect.element(nav.getByText('未分组', { exact: true })).toBeVisible()
  // The group holding CH01 is open, so its rows are reachable without a click.
  await expect
    .element(nav.getByRole('button', { name: /CH02/ }))
    .toBeVisible()
  const disabled = nav.getByRole('button', { name: /CH03/ })
  await expect.element(disabled).toBeVisible()
  // A disabled channel is marked, not hidden: it stays a valid target.
  await userEvent.click(disabled)
  await expect
    .element(view.getByRole('heading', { name: /车库/ }))
    .toBeVisible()
  setup.client.clear()
})

test('the header shows the recording mode read-only and links to the plan page', async () => {
  const setup = fixture({ continuous: true }),
    view = await render(setup.view)
  await expect
    .element(view.getByText('手动（连续录像）', { exact: true }))
    .toBeVisible()
  await expect
    .element(view.getByRole('link', { name: '在录像计划页修改' }))
    .toBeVisible()
  // Naming, grouping and the mode are separate from the source: nothing on this
  // page writes a recording policy.
  expect(
    setup.mutations.some((m) => m.path.includes('recording-policy'))
  ).toBe(false)
  setup.client.clear()
})

test('history and diagnostics open inline instead of on a second tab', async () => {
  const setup = fixture({ continuous: true }),
    view = await render(setup.view)
  await expect
    .element(view.getByRole('button', { name: '历史与诊断', exact: true }))
    .toBeVisible()
  // The source form stays on screen: history is a panel, not a peer tab.
  await expect
    .element(view.getByRole('button', { name: '保存并启用', exact: true }))
    .toBeVisible()
  await userEvent.click(
    view.getByRole('button', { name: '历史与诊断', exact: true })
  )
  await expect
    .element(view.getByRole('heading', { name: '历史修订', exact: true }))
    .toBeVisible()
  setup.client.clear()
})

test('clearing the camera asks for confirmation and keeps the channel slot', async () => {
  const setup = fixture({ continuous: true }),
    view = await render(setup.view)
  await userEvent.click(
    view.getByText('危险操作', { exact: true })
  )
  await userEvent.click(
    view.getByRole('button', { name: '清空摄像头配置', exact: true })
  )
  // The destructive action is behind a confirm dialog, not a bare button.
  await expect
    .element(view.getByText('清空此通道的摄像头？'))
    .toBeVisible()
  expect(
    setup.mutations.some((m) => m.path.endsWith('/source/clear'))
  ).toBe(false)
  setup.client.clear()
})
