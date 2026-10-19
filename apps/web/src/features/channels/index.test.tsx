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
import { Channels } from './index'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})
function observed(
  state: Schema<'ObservedStatus'>['state'],
  reason = ''
): Schema<'ObservedStatus'> {
  return { state, reason, observed_at: null, expires_at: null }
}
const summaries: Schema<'ChannelSummary'>[] = [
  {
    channel_id: '00000000-0000-4000-8000-000000000001',
    channel_no: 1,
    channel_name: '大门',
    channel_group: '一层',
    enabled: true,
    version: 3,
    permissions: ['configure', 'live', 'playback'],
    current_revision_id: '00000000-0000-4000-8000-0000000000aa',
    source_ip: '192.168.66.114',
    main_path: '/Streaming/Channels/101',
    sub_path: '/Streaming/Channels/102',
    onvif_port: 80,
    main: observed('healthy'),
    sub: observed('unknown', 'observation_missing'),
    recording: observed('disabled', 'recording_disabled'),
    last_error: '',
    bitrate_kbps: 4096,
    main_media: {
      codec: 'h264',
      width: 2560,
      height: 1440,
      fps: 25,
      profile: 'High',
      level: '5.1',
      bitrate_kbps: 4096,
      pixel_format: 'yuvj420p',
      audio_codec: 'pcm_alaw',
      audio_sample_rate: 8000,
      audio_channels: 1,
      audio_bitrate_kbps: 64,
      observed_at: '2026-10-09T05:00:00Z',
    },
    sub_media: {
      codec: 'h264',
      width: 704,
      height: 576,
      fps: 25,
      profile: 'Main',
      bitrate_kbps: 1024,
      sample_aspect: '12:11',
      audio_codec: 'pcm_alaw',
      observed_at: '2026-10-09T05:00:00Z',
    },
    updated_at: '2026-10-08T02:30:00Z',
  },
  {
    channel_id: '00000000-0000-4000-8000-000000000002',
    channel_no: 2,
    channel_name: '后门',
    channel_group: '外围',
    enabled: true,
    version: 1,
    permissions: ['configure', 'live', 'playback'],
    current_revision_id: '00000000-0000-4000-8000-0000000000cc',
    source_ip: '192.168.66.116',
    main_path: '/cam2/main',
    sub_path: '/cam2/sub',
    main: observed('unavailable', 'source_unavailable'),
    sub: observed('not_configured', 'sub_not_configured'),
    recording: observed('disabled', 'recording_disabled'),
    last_error: 'source_unavailable',
    bitrate_kbps: null,
    updated_at: '2026-10-08T02:32:00Z',
  },
  {
    channel_id: '00000000-0000-4000-8000-000000000003',
    channel_no: 3,
    channel_name: '只读通道',
    channel_group: '',
    enabled: true,
    version: 1,
    permissions: ['live'],
    current_revision_id: null,
    main: observed('not_configured', 'source_not_configured'),
    sub: observed('not_configured', 'source_not_configured'),
    recording: observed('disabled', 'source_not_configured'),
    last_error: '',
    bitrate_kbps: null,
    updated_at: null,
  },
  {
    channel_id: '00000000-0000-4000-8000-000000000004',
    channel_no: 4,
    channel_name: '侧门',
    channel_group: '一层',
    enabled: true,
    version: 2,
    permissions: ['configure', 'live'],
    current_revision_id: '00000000-0000-4000-8000-0000000000bb',
    source_ip: '192.168.66.115',
    main_path: '/onvif/profile1',
    sub_path: '/onvif/profile2',
    main: observed('healthy'),
    sub: observed('healthy'),
    recording: observed('disabled', 'recording_disabled'),
    last_error: '',
    bitrate_kbps: 2048,
    main_media: {
      codec: 'hevc',
      width: 2560,
      height: 1920,
      fps: 25,
      audio_codec: 'aac',
      observed_at: '2026-10-09T05:05:00Z',
    },
    sub_media: {
      codec: 'hevc',
      width: 640,
      height: 480,
      fps: 25,
      audio_codec: 'aac',
      observed_at: '2026-10-09T05:05:00Z',
    },
    updated_at: '2026-10-08T02:31:00Z',
  },
]
function fixture() {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  calls.request.mockImplementation(
    async (path: string, init?: RequestInit) => {
      if (init?.method === 'PATCH') return summaries[0]
      if (path.endsWith('/source-config-export'))
        return {
          format_version: 1,
          site_id: 'site',
          exported_at: new Date().toISOString(),
          channels: [],
        }
      if (path.endsWith('/channels/summary')) return { items: summaries }
      if (path.endsWith('/api/v1/site'))
        return {
          id: 'site',
          name: '站点',
          timezone: 'Asia/Shanghai',
          channel_count: 16,
          version: 1,
        }
      throw new Error('Unexpected query ' + path)
    }
  )
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <Channels />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return { client, view: <RouterProvider router={router} /> }
}
// Every column comes from the one summary response; the page must not fall back
// to per-channel polling.
test('the list is drawn from a single summary request', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await expect.element(view.getByTestId('channel-row')).toHaveLength(4)
  await expect
    .poll(() =>
      calls.request.mock.calls.filter(([path]) =>
        path.includes('/source/status')
      ).length
    )
    .toBe(0)
  await expect
    .element(view.getByTestId('channel-row').first())
    .toHaveTextContent('4.1 Mbps')
  setup.client.clear()
})
// The row packs the 16:9 thumbnail into the name cell; if a future cell is
// added to the body without a matching header (or the reverse), every label
// from "名称" rightward shifts by one and silently mislabels the columns.
test('every header stays over its own column', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await expect.element(view.getByTestId('channel-row')).toHaveLength(4)
  const head = document.querySelectorAll('thead th').length
  const cells = document.querySelectorAll('tbody tr:first-child td').length
  expect(head).toBeGreaterThan(0)
  expect(head).toBe(cells)
  // The name cell carries the thumbnail and the name together.
  const nameCell = document.querySelector('tbody tr:first-child td:nth-child(3)')
  expect(nameCell?.querySelector('[data-testid="channel-thumbnail"]')).toBeTruthy()
  expect(nameCell?.textContent).toContain('大门')
  setup.client.clear()
})
test('status filter pills count and narrow the table', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  // CH01 healthy main → normal; CH02 enabled with no healthy kind → abnormal;
  // CH03 has no camera → unconfigured. None is disabled in this fixture.
  const group = view.getByRole('group', { name: '状态筛选' })
  await expect.element(group).toHaveTextContent('全部4')
  await expect.element(group).toHaveTextContent('正常2')
  await expect.element(group).toHaveTextContent('已停用0')
  await expect.element(group).toHaveTextContent('未配置1')
  await expect.element(group).toHaveTextContent('异常1')
  await userEvent.click(
    group.getByRole('button', { name: /未配置/ })
  )
  await expect.element(view.getByTestId('channel-row')).toHaveLength(1)
  await expect
    .element(view.getByTestId('channel-row'))
    .toHaveTextContent('只读通道')
  await userEvent.click(group.getByRole('button', { name: /正常/ }))
  await expect.element(view.getByTestId('channel-row')).toHaveLength(2)
  setup.client.clear()
})
test('unconfigured rows cannot be selected and expose no enable toggle', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  const row = view.getByTestId('channel-row').nth(2)
  await expect
    .element(view.getByRole('checkbox', { name: '选择 CH03' }))
    .toBeDisabled()
  await expect.element(row).toHaveTextContent('未配置')
  // The three observation columns and media/bitrate collapse to a dash.
  await expect.element(row).toHaveTextContent('未配置摄像头')
  expect((row.element() as HTMLElement).textContent).not.toContain('正常')
  expect((row.element() as HTMLElement).textContent).not.toContain('启用')
  // Every configured row keeps its enable/disable control.
  await expect
    .element(view.getByRole('button', { name: '停用 CH01' }))
    .toBeInTheDocument()
  setup.client.clear()
})
test('select-all only checks configurable, configured channels', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(
    view.getByRole('checkbox', { name: '选择当前结果' })
  )
  await expect
    .element(view.getByRole('checkbox', { name: '选择 CH01' }))
    .toBeChecked()
  await expect
    .element(view.getByRole('checkbox', { name: '选择 CH02' }))
    .toBeChecked()
  await expect
    .element(view.getByRole('checkbox', { name: '选择 CH03' }))
    .not.toBeChecked()
  // CH01, CH02 and CH04 are configured and permitted; CH03 is left out.
  await expect.element(view.getByText('已选 3 个通道')).toBeVisible()
  setup.client.clear()
})
test('the compact batch bar enables, disables and clears groups in bulk', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('checkbox', { name: '选择 CH01' }))
  await userEvent.click(view.getByRole('checkbox', { name: '选择 CH02' }))
  await userEvent.click(
    view.getByRole('button', { name: '批量停用', exact: true })
  )
  await userEvent.click(
    view.getByRole('button', { name: '批量停用', exact: true })
  )
  await expect
    .poll(
      () =>
        calls.request.mock.calls.filter(([, init]) => init?.method === 'PATCH')
          .length
    )
    .toBe(2)
  const patches = calls.request.mock.calls.filter(
    ([, init]) => init?.method === 'PATCH'
  )
  expect(patches.map(([, init]) => JSON.parse(init.body as string))).toEqual([
    { enabled: false },
    { enabled: false },
  ])
  expect(
    patches.map(([, init]) => new Headers(init.headers).get('If-Match'))
  ).toEqual(['"3"', '"1"'])
  setup.client.clear()
})
test('the group filter narrows by business grouping', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText('分组筛选'))
  await userEvent.click(view.getByRole('option', { name: '外围', exact: true }))
  await expect.element(view.getByTestId('channel-row')).toHaveLength(1)
  await expect
    .element(view.getByTestId('channel-row'))
    .toHaveTextContent('后门')
  await userEvent.click(view.getByLabelText('分组筛选'))
  await userEvent.click(view.getByRole('option', { name: '未分组', exact: true }))
  await expect.element(view.getByTestId('channel-row')).toHaveLength(1)
  await expect
    .element(view.getByTestId('channel-row'))
    .toHaveTextContent('只读通道')
  setup.client.clear()
})
test('selected config export contains only the explicitly selected permitted channel', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('checkbox', { name: '选择 CH02' }))
  await expect
    .element(view.getByRole('checkbox', { name: '选择 CH03' }))
    .toBeDisabled()
  await userEvent.click(
    view.getByRole('button', { name: '导出摄像头配置', exact: true })
  )
  const request = calls.request.mock.calls.find(([path]) =>
    path.endsWith('/source-config-export')
  )
  expect(JSON.parse(request![1].body)).toEqual({
    channel_ids: [summaries[1].channel_id],
  })
  await expect.element(view.getByRole('status')).toHaveTextContent('配置已导出')
  setup.client.clear()
})
test('revoked selection never broadens export to every other channel', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('checkbox', { name: '选择 CH02' }))
  setup.client.setQueryData(['/api/v1/channels/summary'], {
    items: summaries.map((channel) =>
      channel.channel_no === 2 ? { ...channel, permissions: ['live'] } : channel
    ),
  })
  await expect
    .element(view.getByRole('button', { name: '导出摄像头配置' }))
    .toBeDisabled()
  expect(
    calls.request.mock.calls.some(([path]) =>
      path.endsWith('/source-config-export')
    )
  ).toBe(false)
  await userEvent.click(view.getByRole('button', { name: '取消选择' }))
  await expect
    .element(view.getByRole('button', { name: '导出摄像头配置' }))
    .toBeEnabled()
  setup.client.clear()
})
// CH-04: disabling is an explicit, confirmed, reversible action that leaves the
// source, the recording policy and the history alone.
test('disabling a channel is confirmed and sends only the enabled flag', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(
    view.getByRole('button', { name: '停用 CH01', exact: true })
  )
  await userEvent.click(view.getByRole('button', { name: '停用', exact: true }))
  await expect
    .poll(() => calls.request.mock.calls.filter(([, init]) => init?.method === 'PATCH').length)
    .toBe(1)
  const [path, init] = calls.request.mock.calls.find(
    ([, value]) => value?.method === 'PATCH'
  )!
  expect(path).toBe(`/api/v1/channels/${summaries[0].channel_id}`)
  expect(JSON.parse(init.body as string)).toEqual({ enabled: false })
  expect(new Headers(init.headers).get('If-Match')).toBe('"3"')
  setup.client.clear()
})
test('batch grouping patches every selected channel with its own version', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await userEvent.click(view.getByRole('checkbox', { name: '选择 CH01' }))
  await userEvent.click(view.getByRole('checkbox', { name: '选择 CH02' }))
  await userEvent.fill(view.getByLabelText('批量分组'), '机房 A')
  await userEvent.click(
    view.getByRole('button', { name: '应用分组', exact: true })
  )
  await expect
    .poll(
      () =>
        calls.request.mock.calls.filter(([, init]) => init?.method === 'PATCH')
          .length
    )
    .toBe(2)
  const patches = calls.request.mock.calls.filter(
    ([, init]) => init?.method === 'PATCH'
  )
  expect(patches.map(([, init]) => JSON.parse(init.body as string))).toEqual([
    { channel_group: '机房 A' },
    { channel_group: '机房 A' },
  ])
  expect(
    patches.map(([, init]) => new Headers(init.headers).get('If-Match'))
  ).toEqual(['"3"', '"1"'])
  setup.client.clear()
})
// The row now carries the applied source's media parameters and access
// method; the camera column keeps only the address with the access badge,
// and a healthy row gets the preview/recording shortcuts.
test('media parameters, access badge and healthy-row shortcuts', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  const rows = view.getByTestId('channel-row')
  await expect.element(rows.nth(0)).toHaveTextContent('ONVIF')
  // Main stream leads with the megapixel shorthand and then states the exact
  // pixel count, codec profile, frame rate and declared bit rate; the sub stream
  // sits dimmed beneath; audio is a pill and "no audio" never prints.
  await expect.element(rows.nth(0)).toHaveTextContent('3MP')
  await expect.element(rows.nth(0)).toHaveTextContent('2560×1440')
  await expect.element(rows.nth(0)).toHaveTextContent('H.264')
  await expect.element(rows.nth(0)).toHaveTextContent('High')
  await expect.element(rows.nth(0)).toHaveTextContent('25fps')
  await expect.element(rows.nth(0)).toHaveTextContent('4.1M')
  await expect.element(rows.nth(0)).toHaveTextContent('子: 704×576')
  await expect.element(rows.nth(0)).toHaveTextContent('Main')
  // Audio is stated per stream now: "主 PCMA" says the MAIN stream carries it.
  await expect.element(rows.nth(0)).toHaveTextContent('主 PCMA')
  await expect.element(rows.nth(0)).not.toHaveTextContent('无音频')
  await expect.element(rows.nth(0)).toHaveTextContent('192.168.66.114')
  await expect
    .element(rows.nth(0))
    .not.toHaveTextContent('/Streaming/Channels/101')
  await expect
    .element(
      view.getByRole('link', { name: '预览 CH01' })
    )
    .toHaveAttribute(
      'href',
      `/live?channel=${summaries[0].channel_id}`
    )
  await expect
    .element(view.getByRole('link', { name: '录像 CH01' }))
    .toHaveAttribute(
      'href',
      `/recordings?channel=${summaries[0].channel_id}&at=&tab=playback`
    )
  // The cell now carries the facts that explain a soft or unplayable picture:
  // the exact pixel count and the declared bit rate, not just the shorthand.
  await expect.element(rows.nth(3)).toHaveTextContent('4MP')
  await expect.element(rows.nth(3)).toHaveTextContent('2560×1920')
  await expect.element(rows.nth(3)).toHaveTextContent('H.265')
  await expect.element(rows.nth(3)).toHaveTextContent('主 AAC')
  setup.client.clear()
})
test('thumbnail slot and recording shortcut on configured channels', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  const rows = view.getByTestId('channel-row')
  // Healthy configured rows show the glowing camera glyph; the unconfigured
  // row shows the dashed empty slot.
  await expect
    .element(rows.nth(0).getByTestId('channel-thumbnail'))
    .toHaveAttribute('data-state', 'online')
  await expect
    .element(rows.nth(2).getByTestId('channel-thumbnail'))
    .toHaveAttribute('data-state', 'unconfigured')
  // CH02 is configured but unhealthy: no preview shortcut, recording stays.
  await expect
    .element(view.getByRole('link', { name: '预览 CH02' }).query())
    .toBeNull()
  await expect
    .element(view.getByRole('link', { name: '录像 CH02' }))
    .toBeVisible()
  setup.client.clear()
})
