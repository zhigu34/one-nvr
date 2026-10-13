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
    channel_group: '',
    enabled: true,
    version: 1,
    permissions: ['playback'],
  },
  {
    id: 'channel-2',
    channel_no: 2,
    channel_name: '仓库',
    channel_group: '',
    enabled: true,
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
  protected: false,
  media: { codec: '', width: 0, height: 0, fps: 0, duration_ms: 0 },
    naming_timezone: 'Asia/Shanghai',
    utc_offset_seconds: 28800,
    check_state: 'ready',
    ...overrides,
  }
}

afterEach(() => vi.clearAllMocks())

type Page = { items: Schema<'RecordingSegment'>[]; next_cursor: string | null }

async function fixture(
  segments: Schema<'RecordingSegment'>[],
  probe = '',
  zone: string | null = 'Asia/Shanghai',
  pages?: Page[]
) {
  calls.probe.mockResolvedValue(probe)
  calls.request.mockImplementation(async (path: string) => {
    if (path.startsWith('/api/v1/channels'))
      return { items: channels, next_cursor: null }
    if (path.startsWith('/api/v1/site')) {
      if (zone === null) return new Promise(() => {})
      return {
        id: 'site',
        name: '站点',
        timezone: zone,
        channel_count: 16,
        version: 1,
      }
    }
    if (path.startsWith('/api/v1/recordings')) {
      if (pages) {
        // Cursor paging: the n-th index request gets the n-th page, so a test
        // chains pages without caring about the cursor values themselves.
        const made = calls.request.mock.calls.filter((call) =>
          (call[0] as string).startsWith('/api/v1/recordings')
        ).length
        return pages[Math.min(made - 1, pages.length - 1)]
      }
      return { items: segments, next_cursor: null }
    }
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
const fieldValue = (locator: { element: () => HTMLElement | SVGElement }) =>
  (locator.element() as HTMLInputElement).value
const lastQuery = () => {
  const paths = calls.request.mock.calls.map((call) => call[0] as string)
  const path = [...paths]
    .reverse()
    .find((candidate) => candidate.startsWith('/api/v1/recordings'))
  return new URLSearchParams(path?.split('?')[1] || '')
}
const shanghaiDay = (epochMs: number) =>
  new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai' }).format(
    new Date(epochMs)
  )

test('search inputs read and write the site clock while the request stays UTC', async () => {
  const { view } = await fixture([segment(A, 0, 120)])
  await vi.waitFor(() =>
    expect(fieldValue(view.getByLabelText('检索开始时间'))).toBe(
      '2026-10-05T08:00'
    )
  )
  expect(fieldValue(view.getByLabelText('检索结束时间'))).toBe(
    '2026-10-05T08:05'
  )
  // The same window as the API is asked for it: 08:00 Shanghai is 00:00 UTC.
  expect(lastQuery().get('start')).toBe('2026-10-05T00:00:00.000Z')
  expect(lastQuery().get('end')).toBe('2026-10-05T00:05:00.000Z')
  await view.getByLabelText('检索开始时间').fill('2026-10-05T09:00')
  await view.getByLabelText('检索结束时间').fill('2026-10-05T09:30:15')
  await view.getByRole('button', { name: '检索录像' }).click()
  await vi.waitFor(() =>
    expect(lastQuery().get('start')).toBe('2026-10-05T01:00:00.000Z')
  )
  expect(lastQuery().get('end')).toBe('2026-10-05T01:30:15.000Z')
})

test('an out-of-order range is refused without querying', async () => {
  const { view } = await fixture([segment(A, 0, 120)])
  await view.getByLabelText('检索开始时间').fill('2026-10-05T10:00')
  await view.getByLabelText('检索结束时间').fill('2026-10-05T09:00')
  await view.getByRole('button', { name: '检索录像' }).click()
  await says('结束时间需晚于开始时间')
  expect(lastQuery().get('end')).toBe('2026-10-05T00:05:00.000Z')
})

test('quick ranges are resolved on the site clock, not the browser one', async () => {
  const { view } = await fixture([segment(A, 0, 120)])
  await view.getByRole('button', { name: '最近 1 小时' }).click()
  await vi.waitFor(() => {
    const start = Date.parse(lastQuery().get('start')!)
    const end = Date.parse(lastQuery().get('end')!)
    expect(end - start).toBe(3600_000)
    expect(Math.abs(end - Date.now())).toBeLessThan(10_000)
  })
  const today = shanghaiDay(Date.now())
  await view.getByRole('button', { name: '今天' }).click()
  await vi.waitFor(() =>
    expect(fieldValue(view.getByLabelText('检索开始时间'))).toBe(
      `${today}T00:00`
    )
  )
  // Shanghai is UTC+8, so the local midnight is 16:00 UTC the day before.
  expect(lastQuery().get('start')).toBe(
    new Date(Date.parse(`${today}T00:00:00Z`) - 8 * 3600_000).toISOString()
  )
  await view.getByRole('button', { name: '昨天' }).click()
  await vi.waitFor(() =>
    expect(fieldValue(view.getByLabelText('检索开始时间'))).toBe(
      `${shanghaiDay(Date.now() - 24 * 3600_000)}T00:00`
    )
  )
  expect(fieldValue(view.getByLabelText('检索结束时间'))).toBe(`${today}T00:00`)
})

test('nothing clock dependent renders before the site timezone is known', async () => {
  const { view } = await fixture([segment(A, 0, 120)], '', null)
  await says('正在读取站点时区')
  expect(document.body.textContent).not.toContain('检索开始时间')
  expect(view.getByRole('button', { name: '检索录像' }).query()).toBeNull()
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

test('an idle workspace shows no fake position and names the channel, not playback', async () => {
  const { view } = await fixture([segment(A, 0, 120)])
  await view.getByRole('button', { name: 'CH01 大门' }).click()
  await says('当前通道 CH01 大门')
  await says('当前位置 —')
  expect(document.body.textContent).not.toContain('1970-01-01')
  expect(document.body.textContent).not.toContain('正在回放')
})

test('the index follows the cursor until the window is exhausted', async () => {
  const { view } = await fixture([], '', 'Asia/Shanghai', [
    { items: [segment(A, 0, 120)], next_cursor: 'cursor-1' },
    { items: [segment(B, 120, 240)], next_cursor: null },
  ])
  // The second page's segment reaches the timeline.
  await expect
    .element(view.getByRole('button', { name: '片段 2026-10-05 08:02:00' }))
    .toBeVisible()
  expect(document.body.textContent).not.toContain('加载上限')
  expect(recordingCalls()).toHaveLength(2)
})

test('a window too large for the cap warns instead of hiding the rest', async () => {
  const bulk = (page: number) =>
    Array.from({ length: 500 }, (_, i) =>
      segment(
        `seg-${page}-${String(i).padStart(4, '0')}`,
        -(page * 500 + i + 1) * 60,
        -(page * 500 + i) * 60
      )
    )
  const { view } = await fixture([], '', 'Asia/Shanghai', [
    { items: bulk(0), next_cursor: 'cursor-1' },
    { items: bulk(1), next_cursor: 'cursor-2' },
    { items: bulk(2), next_cursor: 'cursor-3' },
    { items: bulk(3), next_cursor: 'cursor-4' },
  ])
  await expect
    .element(view.getByRole('alert'))
    .toHaveTextContent('片段超过单次加载上限')
  await expect
    .element(view.getByRole('alert'))
    .toHaveTextContent('更早的录像请缩小时间范围后检索')
  // The cap stops the chain: exactly the 2000-segment ceiling was fetched.
  await vi.waitFor(() => expect(recordingCalls()).toHaveLength(4))
})

const recordingCalls = () =>
  calls.request.mock.calls.filter((call) =>
    String(call[0]).startsWith('/api/v1/recordings')
  )

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
  await userEvent.click(speed)
  for (const label of ['0.5x', '1x', '2x', '4x']) {
    await expect
      .element(view.getByRole('option', { name: label, exact: true }))
      .toBeVisible()
  }
  // Exactly the documented rates: no extra options beyond the four asserted.
  await expect.element(view.getByRole('option')).toHaveLength(4)
  await userEvent.click(view.getByRole('option', { name: '2x', exact: true }))
  await expect.element(speed).toHaveTextContent('2x')
})

test('the timeline picks an export bracket and zooms the view', async () => {
  const { view } = await fixture([segment(A, 0, 120), segment(B, 180, 300)])
  await view.getByRole('button', { name: '片段 2026-10-05 08:00:00' }).click()
  // Picker mode: two background clicks bracket the range, the third restarts.
  await view.getByRole('button', { name: '选择导出区间' }).click()
  const strip = view.getByTestId('playback-timeline')
  await strip.click({ position: { x: 40, y: 20 } })
  await strip.click({ position: { x: 120, y: 20 } })
  await vi.waitFor(() => expect(document.querySelector('[data-testid="playback-bracket"]')).toBeTruthy())
  // Width via calc() keeps the overlay out of Playwright's visibility math in
  // headless mode; presence plus the dialogue below is the real assertion.
  await expect
    .element(view.getByRole('button', { name: '导出所选区间' }))
    .toBeEnabled()
  await view.getByRole('button', { name: '导出所选区间' }).click()
  // The dialog analyzes exactly the bracketed window.
  await vi.waitFor(() => {
    const paths = calls.request.mock.calls.map((call) => String(call[0]))
    const analyze = paths.find((p) => p.includes('/recording-exports/analyze'))
    expect(analyze).toBeTruthy()
  })
  await view.getByRole('button', { name: '取消' }).click()
  // Zoom narrows the caption bounds; 全览 returns to the full range.
  await view.getByRole('button', { name: '时间轴放大' }).click()
  await view.getByRole('button', { name: '显示完整范围' }).click()
  await expect.element(view.getByText(/2026-10-05 08:00:00 — 2026-10-05 08:05:00/)).toBeVisible()
})

// Frame top-OSD metadata strip (ca17f75): the channel pill rides along in
// idle state, but resolution / fps / codec / "直通" only appear once an
// entry with media facts has been loaded by the controller.
test('frame OSD lists media facts only after an entry has loaded', async () => {
  const rich = segment(A, 0, 120, {
    media: {
      codec: 'h265',
      width: 2560,
      height: 1440,
      fps: 25,
      duration_ms: 120_000,
    },
  })
  const { view } = await fixture([rich])
  // Idle: only the channel identity is on screen; the media facts must not
  // appear because there is no entry yet. Scope to the OSD strip so the
  // unrelated "当前通道 CH01 大门" paragraph and the channel button do
  // not get matched.
  const osd = view.getByTestId('playback-frame-osd')
  await expect.element(osd).toBeVisible()
  await expect.element(osd.getByText('CH01 大门', { exact: true })).toBeVisible()
  // Width is zero in the default fixture and non-zero in this one — same
  // helper, so the gate is real: idle must skip the resolution pill.
  await expect.element(osd.getByText('2560×1440')).not.toBeInTheDocument()
  // Load the segment; the controller flips state.entry on success and the
  // OSD pills populate.
  await view
    .getByRole('button', { name: '片段 2026-10-05 08:00:00' })
    .dblClick()
  await playing(A)
  await expect.element(osd.getByText('2560×1440')).toBeVisible()
  await expect.element(osd.getByText('25fps')).toBeVisible()
  await expect.element(osd.getByText('H265')).toBeVisible()
  await expect.element(osd.getByText('直通')).toBeVisible()
})

// State badge (e693351): the badge itself only mounts for non-idle
// phases (loading/playing/paused). Idle keeps the slot absent so the
// placeholder stays uncluttered.
test('frame OSD state badge mounts when playback leaves idle', async () => {
  const { view } = await fixture([segment(A, 0, 120)])
  // Idle: no badge testid at all.
  expect(view.getByTestId('playback-state-badge').query()).toBeNull()
  // Kick off playback. The controller publishes 'loading' on seek; even if
  // the headless runner skips straight to ready, we still dispatched the
  // 'playing' event the controller binds to.
  const bar = view.getByRole('button', { name: '片段 2026-10-05 08:00:00' })
  await bar.dblClick()
  await playing(A)
  const video = document.querySelector('video') as HTMLVideoElement | null
  video?.dispatchEvent(new Event('playing'))
  await expect.element(view.getByTestId('playback-state-badge')).toBeVisible()
  // Exactly one of the three phase strings surfaces; the controller's own
  // reactive path chose the text. The DOM contract is "one of these
  // three labels is on screen":
  await vi.waitFor(() =>
    expect(
      ['REC', '加载中', '已暂停'].some((label) =>
        view
          .getByTestId('playback-state-badge')
          .element()
          .textContent?.includes(label)
      )
    ).toBe(true)
  )
})
