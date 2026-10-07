import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import type { Schema } from '@/lib/types'
import { Playback } from './index'

const calls = vi.hoisted(() => ({ request: vi.fn(), probe: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
vi.mock('./api', async (original) => ({
  ...(await original<typeof import('./api')>()),
  probeSegment: calls.probe,
}))

const base = Date.parse('2026-10-05T00:00:00Z')
const at = (seconds: number) => new Date(base + seconds * 1000).toISOString()
const anchor = at(300)
const A = 'aaaaaaaa-0000-4000-8000-000000000001'
const B = 'bbbbbbbb-0000-4000-8000-000000000002'
const DAMAGED = 'dddddddd-0000-4000-8000-000000000003'
const READY = 'eeeeeeee-0000-4000-8000-000000000004'

const channels: Schema<'Channel'>[] = [
  {
    id: 'channel-1',
    channel_no: 1,
    channel_name: '大门',
    version: 1,
    permissions: ['playback'],
  },
  {
    id: 'channel-2',
    channel_no: 2,
    channel_name: '仓库',
    version: 1,
    permissions: ['live'],
  },
]

function segment(
  id: string,
  from: number,
  to: number,
  overrides: Partial<Schema<'RecordingSegment'>> = {}
): Schema<'RecordingSegment'> {
  return {
    id,
    channel_id: 'channel-1',
    source_revision_id: 'revision',
    pool_id: 'pool',
    run_id: 'run',
    start: at(from),
    end: at(to),
    bytes: 2048,
    state: 'ready',
    naming_timezone: 'Asia/Shanghai',
    utc_offset_seconds: 28800,
    check_state: 'ready',
    ...overrides,
  }
}

afterEach(() => vi.clearAllMocks())

async function fixture(segments: Schema<'RecordingSegment'>[], probe = '') {
  calls.probe.mockResolvedValue(probe)
  calls.request.mockImplementation((path: string) => {
    if (path.startsWith('/api/v1/channels'))
      return { items: channels, next_cursor: null }
    if (path.startsWith('/api/v1/site'))
      return {
        id: 'site',
        name: '站点',
        timezone: 'Asia/Shanghai',
        channel_count: 16,
        version: 1,
      }
    if (path.startsWith('/api/v1/recordings'))
      return { items: segments, next_cursor: null }
    throw new Error('Unexpected query ' + path)
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <Playback initialAt={anchor} />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  const view = await render(<RouterProvider router={router} />)
  return {
    view,
    video: () => document.querySelector('video') as HTMLVideoElement,
  }
}

const playing = (id: string) =>
  vi.waitFor(() =>
    expect((document.querySelector('video') as HTMLVideoElement).src).toContain(
      `/api/v1/recordings/${id}/content`
    )
  )
const says = (text: string) =>
  vi.waitFor(() => expect(document.body.textContent).toContain(text), {
    timeout: 10_000,
  })

test('offers only channels with playback permission and starts no media on entry', async () => {
  const { view, video } = await fixture([segment(A, 0, 120)])
  await expect
    .element(view.getByRole('button', { name: 'CH01 大门' }))
    .toBeVisible()
  expect(document.body.textContent).not.toContain('CH02 仓库')
  // PLAY-09: an entry point loads the index only.
  expect(video().src).toBe('')
  expect(video().currentTime).toBe(0)
})

test('a single click only selects a segment, a double click plays it', async () => {
  const { view, video } = await fixture([segment(A, 0, 120)])
  const bar = view.getByRole('button', { name: '片段 2026-10-05 08:00:00' })
  await bar.click()
  await says('片段 aaaaaaaa')
  expect(video().src).toBe('')
  await bar.dblClick()
  await playing(A)
})

test('a hole is reported with its bounds and can be crossed only on request', async () => {
  const { view } = await fixture([segment(A, 0, 10), segment(B, 120, 130)])
  await view.getByRole('button', { name: '播放首个可用片段' }).click()
  await playing(A)
  // Ten seconds past a ten second segment lands in the hole, not on a frame.
  await view.getByRole('button', { name: '+10 秒' }).click()
  await says('该时间没有可播放录像')
  expect(document.body.textContent).toContain('2026-10-05 08:00:10')
  await view.getByRole('button', { name: '跳到下一可用片段' }).click()
  await playing(B)
})

test('a segment the index cannot vouch for is blocked, never offered', async () => {
  const { view, video } = await fixture([
    segment(DAMAGED, 0, 60, { state: 'damaged' }),
    segment(READY, 60, 120),
  ])
  await view.getByRole('button', { name: '片段 2026-10-05 08:00:00' }).click()
  await says('文件损坏，与发布记录不一致')
  await view.getByRole('button', { name: '从下一可用片段开始播放' }).click()
  await playing(READY)
  expect(video().src).not.toContain(DAMAGED)
})

test('an unavailable segment is explained with the reason the API returns', async () => {
  const { view } = await fixture([segment(A, 0, 120)], '资源不存在')
  await view
    .getByRole('button', { name: '片段 2026-10-05 08:00:00' })
    .dblClick()
  await says('资源不存在')
  expect(calls.probe).toHaveBeenCalled()
})

test('speed control offers only the documented rates', async () => {
  const { view } = await fixture([segment(A, 0, 120)])
  const speed = view.getByLabelText('播放倍速')
  await expect.element(speed).toBeVisible()
  const values = [...document.querySelectorAll('select option')].map(
    (option) => (option as HTMLOptionElement).value
  )
  expect(values).toEqual(['0.5', '1', '2', '4'])
  await speed.selectOptions('2')
  expect((speed.element() as HTMLSelectElement).value).toBe('2')
})
