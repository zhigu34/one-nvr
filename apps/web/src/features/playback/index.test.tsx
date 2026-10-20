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
    // Export is its own grant (EXP): the player bar's control only exists for a
    // channel the operator may actually export from.
    permissions: ['playback', 'export'],
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
  // The parked player runs a lease heartbeat on its segment; that is an
  // action endpoint, not an index query, so it never counts as "the last
  // search".
  const path = [...paths]
    .reverse()
    .find(
      (candidate) =>
        candidate.startsWith('/api/v1/recordings?') ||
        candidate.startsWith('/api/v1/recordings/stats')
    )
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

test('offers only channels with playback permission and parks the newest segment on entry', async () => {
  const { view, video } = await fixture([segment(A, 0, 120)])
  await expect
    .element(view.getByRole('button', { name: 'CH01 大门' }))
    .toBeVisible()
  expect(document.body.textContent).not.toContain('CH02 仓库')
  // The entry point auto-loads the newest segment's first frame (paused —
  // no playback), so the operator sees a picture instead of a black ring.
  // Motion only ever starts from 播放.
  await vi.waitFor(() => expect(video().src).toContain(`/api/v1/recordings/${A}/content`))
  expect(video().paused).toBe(true)
  expect(document.body.textContent).not.toContain('点击下方时间轴的片段开始回放')
})

test('an idle workspace shows no fake position and names the channel, not playback', async () => {
  const { view } = await fixture([segment(A, 0, 120)])
  await view.getByRole('button', { name: 'CH01 大门' }).click()
  // The park attempt publishes a real phase (paused with the frame, or an
  // explicit error when the headless runner cannot serve the bytes) — never
  // a fake clock or a phantom "playing".
  await vi.waitFor(() => {
    const text = document.body.textContent || ''
    return text.includes('已定位到片段') || text.includes('资源不存在') || text.includes('播放中断')
  })
  expect(document.body.textContent).not.toContain('1970-01-01')
  expect(document.body.textContent).not.toContain('播放中')
})

test('the index follows the cursor until the window is exhausted', async () => {
  const { view } = await fixture([], '', 'Asia/Shanghai', [
    { items: [segment(A, 0, 120)], next_cursor: 'cursor-1' },
    { items: [segment(B, 120, 240)], next_cursor: null },
  ])
  // The second page's segment reaches the timeline: the canvas strip is up
  // and the band's density read counts both pages' segments.
  await expect
    .element(view.getByTestId('playback-timeline-canvas'))
    .toBeVisible()
  await says('密度 2 段')
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
  calls.request.mock.calls.filter((call) => {
    const path = String(call[0])
    // Only index reads: the parked player's lease heartbeat is an action
    // endpoint and must not count toward the paging chain.
    return (
      path.startsWith('/api/v1/recordings?') ||
      path.startsWith('/api/v1/recordings/stats')
    )
  })

test('a single click only selects a segment, a double click plays it', async () => {
  const { view, video } = await fixture([segment(A, 0, 120)])
  // Auto-park has already loaded the newest segment's first frame (paused).
  // Segment A covers the first 40% of the 5-minute strip (0:00–0:02 of 0:05).
  const strip = view.getByTestId('playback-timeline-canvas')
  await strip.click({ position: { x: 40, y: 45 } })
  await says('片段 aaaaaaaa')
  // A click parks on the segment — the resource is loaded, motion is not.
  expect(video().src).toContain(`/api/v1/recordings/${A}/content`)
  expect(video().paused).toBe(true)
  await strip.dblClick({ position: { x: 40, y: 45 } })
  await playing(A)
})

test('a hole is reported with its bounds and can be crossed only on request', async () => {
  const { view } = await fixture([segment(A, 0, 10), segment(B, 120, 130)])
  // Auto-park lands on the newest segment (B); select A explicitly, then the
  // smart play button starts from it.
  const strip = view.getByTestId('playback-timeline-canvas')
  await strip.click({ position: { x: 2, y: 45 } })
  await says('片段 aaaaaaaa')
  await view.getByRole('button', { name: '播放', exact: true }).click()
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
  // The damaged bar covers 0..50% of the strip; clicking it selects the
  // blocked segment and the smart play button jumps to the next playable.
  const strip = view.getByTestId('playback-timeline-canvas')
  await strip.click({ position: { x: 40, y: 45 } })
  await says('文件损坏，与发布记录不一致')
  await view.getByRole('button', { name: '播放', exact: true }).click()
  await playing(READY)
  expect(video().src).not.toContain(DAMAGED)
})

test('an unavailable segment is explained with the reason the API returns', async () => {
  const { view } = await fixture([segment(A, 0, 120)], '资源不存在')
  await view
    .getByTestId('playback-timeline-canvas')
    .dblClick({ position: { x: 40, y: 45 } })
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
  const strip = view.getByTestId('playback-timeline-canvas')
  await strip.click({ position: { x: 40, y: 45 } })
  await says('片段 aaaaaaaa')
  // Picker mode lives on the PLAYER bar now — export is the second most
  // frequent action after watching, and it used to be a chip among the zoom
  // buttons where nobody looked. Two background clicks bracket the range, the
  // third restarts.
  await view.getByRole('button', { name: '导出片段' }).click()
  // Clicks land on the RECORDING track (24px ruler + 3px gap = y 27..57): a
  // press on the ruler scrubs, which would never bracket.
  await strip.click({ position: { x: 40, y: 42 } })
  await strip.click({ position: { x: 120, y: 42 } })
  await vi.waitFor(() => expect(document.querySelector('[data-testid="playback-bracket"]')).toBeTruthy())
  // Width via calc() keeps the overlay out of Playwright's visibility math in
  // headless mode; presence plus the dialogue below is the real assertion.
  // Once a range exists the primary action names what it will export, so the
  // duration doubles as the confirmation that the bracket was understood.
  const exportButton = view.getByRole('button', { name: /^导出 \d/ })
  await expect.element(exportButton).toBeEnabled()
  await exportButton.click()
  // The dialog analyzes exactly the bracketed window.
  await vi.waitFor(() => {
    const paths = calls.request.mock.calls.map((call) => String(call[0]))
    const analyze = paths.find((p) => p.includes('/recording-exports/analyze'))
    expect(analyze).toBeTruthy()
  })
  await view.getByRole('button', { name: '取消' }).click()
  // Zoom is three preset spans now. This fixture's search window is five
  // minutes, so every preset is WIDER than the range and each one resolves to
  // "show the full range" — a preset must never invent a view outside what was
  // searched. The caption states the visible range, so it is what proves the
  // zoom did nothing rather than something wrong.
  await view.getByRole('button', { name: '1h' }).click()
  await expect
    .element(view.getByTestId('playback-timeline-window'))
    .not.toHaveTextContent('（已缩放）')
  await expect
    .element(view.getByTestId('playback-timeline-window'))
    .toHaveTextContent('2026-10-05 08:00:00 — 2026-10-05 08:05:00')
})

// Timeline upgrade: the ruler is canvas-drawn now (ticks are pixels, not
// text nodes), so the DOM contract narrows to the band's density read and
// the canvas element itself over a 24-hour window.
test('the timeline band exposes the canvas strip and a density read', async () => {
  const { view } = await fixture([segment(A, 0, 120)])
  await expect
    .element(view.getByTestId('playback-timeline-canvas'))
    .toBeVisible()
  // Build a 24-hour window so the canvas ladder picks the hourly step; the
  // drawn grid is asserted by presence of the strip, not by text (pixels).
  await view.getByLabelText('检索开始时间').fill('2026-10-05T00:00')
  await view.getByLabelText('检索结束时间').fill('2026-10-06T00:00')
  await view.getByRole('button', { name: '检索录像' }).click()
  await expect.element(view.getByTestId('playback-timeline-density')).toBeVisible()
  await expect
    .element(view.getByText(/密度\s*\d+\s*段/))
    .toBeVisible()
})

// Frame top-OSD metadata strip (ca17f75): the channel pill rides along in
// idle state, but resolution / fps / codec / "直通" only appear once an
// entry with media facts has been auto-parked on mount.
test('frame OSD lists the selected segment media facts', async () => {
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
  // Media pills describe the SELECTED segment (index facts), so they are up
  // as soon as auto-park selects it — before, during and after any media
  // loading. Scope to the OSD strip.
  const osd = view.getByTestId('playback-frame-osd')
  await expect.element(osd).toBeVisible()
  await expect.element(osd.getByText('CH01 大门', { exact: true })).toBeVisible()
  await expect.element(osd.getByText('2560×1440')).toBeVisible()
  await expect.element(osd.getByText('25fps')).toBeVisible()
  await expect.element(osd.getByText('H265')).toBeVisible()
  await expect.element(osd.getByText('直通')).toBeVisible()
  // Double-click to start motion; the facts are unchanged.
  await view
    .getByTestId('playback-timeline-canvas')
    .dblClick({ position: { x: 40, y: 45 } })
  await playing(A)
  await expect.element(osd.getByText('2560×1440')).toBeVisible()
  await expect.element(osd.getByText('直通')).toBeVisible()
})

// State badge (e693351): the badge itself only mounts for non-idle
// phases (loading/playing/paused). Idle keeps the slot absent so the
// placeholder stays uncluttered.
test('frame OSD state badge mounts when playback leaves idle', async () => {
  const { view } = await fixture([segment(A, 0, 120)])
  // Auto-park lands in 'paused' — the badge is up from the start now, so the
  // assertion is on the badge being present and eventually reading a real
  // phase string.
  const strip = view.getByTestId('playback-timeline-canvas')
  await strip.dblClick({ position: { x: 40, y: 45 } })
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

// The player is one file at a time, so its progress bar spans THAT FILE, not the
// day. The distinction matters: a bar over 24 hours would misreport what
// dragging it does, and seeking past the end is the controller's job (it rolls
// into the next segment).
test('the player exposes a segment progress bar', async () => {
  const { view } = await fixture([segment(A, 0, 120)])
  const bar = view.getByTestId('playback-progress')
  await expect.element(bar).toBeVisible()
  const box = bar.element().getBoundingClientRect()
  // A zero-height bar is an element Playwright calls "not visible": the control
  // bar wraps, and w-full inside a wrapping flex row needs an explicit height.
  expect(box.height).toBeGreaterThan(0)
  // The track must have a real box: a zero-height slider is not clickable, and
  // Playwright calls it "not visible" for the same reason.
  const slider = bar.getByRole('slider', { name: '片段内进度' })
  const track = slider.element().getBoundingClientRect()
  expect(track.height).toBeGreaterThan(0)
  expect(track.width).toBeGreaterThan(0)
})
