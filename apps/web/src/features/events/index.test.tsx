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
import type { PageData, Schema } from '@/lib/types'
import { Events } from './index'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})

const channelID = '00000000-0000-4000-8000-000000000001'
const eventID = '00000000-0000-4000-8000-0000000000e1'

function fixture(
  hits: Schema<'RuleHit'>[],
  options: {
    events?: Schema<'Event'>[]
    deliveries?: Schema<'NotificationDelivery'>[]
    // Overrides the event's media verdict so the three states can be rendered
    // without inventing a whole event per case. Typed as the schema's union:
    // a bare string would let a typo'd state compile and silently render as
    // "pending".
    mediaState?: 'pending' | 'linked' | 'partial' | 'unavailable'
    mediaStartAt?: string
  } = {}
) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  const mutations: { path: string; init: RequestInit }[] = []
  const event: Schema<'Event'> =
    options.events?.find((entry) => entry.id === eventID) ?? {
      id: eventID,
      channel_id: channelID,
      source: 'frigate',
      source_id: 'evt-1',
      category: 'person',
      score: 0.82,
      zones: ['门口'],
      started_at: '2026-10-13T12:00:00Z',
      ended_at: '2026-10-13T12:00:12Z',
      snapshot_state: 'gone',
      media_state: options.mediaState ?? 'linked',
      media_start_at: options.mediaStartAt,
    }
  const events: Schema<'Event'>[] = options.events ?? [event]
  // A PATCH mutates the in-memory list so a follow-up render sees the new
  // state, exactly like the server would.
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    const method = init?.method && init.method !== 'GET' ? init.method : 'GET'
    if (method !== 'GET') mutations.push({ path, init: init as RequestInit })
    if (path === '/api/v1/site')
      return { timezone: 'Asia/Shanghai', site_name: '测试站点' }
    if (path.startsWith('/api/v1/channels?'))
      return {
        items: [
          {
            id: channelID,
            channel_no: 5,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 1,
            permissions: ['playback', 'configure'],
            configured: true,
          },
        ],
        next_cursor: null,
      } satisfies PageData<Schema<'Channel'>>
    if (path.startsWith('/api/v1/events?'))
      return { items: events, next_cursor: null }
    if (path === `/api/v1/events/${eventID}`) return { ...event, hits }
    // The drawer asks what happened to the alert; the default is "nothing was
    // told", which is the pre-batch state for an unbound rule.
    if (path === `/api/v1/events/${eventID}/deliveries`) return { items: options.deliveries ?? [] }
    if (path.startsWith('/api/v1/event-hits/')) return {}
    throw new Error(`unexpected path ${path}`)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return { mutations, client }
}

const view = async (client: QueryClient) => {
  // The detail drawer links into playback, so the page needs a router even
  // though the test never navigates.
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <Events />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return render(<RouterProvider router={router} />)
}

// A reserved slot with no camera behind it is setup context, not an option. Every
// page except channel management hides them, and the server refuses to stream from
// one, so offering it in a filter would only produce an empty result.
test('the channel filter lists configured channels only', async () => {
  calls.request.mockImplementation(async (path: string) => {
    if (path === '/api/v1/site') return { timezone: 'Asia/Shanghai', site_name: '测试站点' }
    if (path.startsWith('/api/v1/channels?'))
      return {
        items: [
          {
            id: channelID,
            channel_no: 5,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 1,
            permissions: ['playback', 'configure'],
            configured: true,
          },
          {
            id: '00000000-0000-4000-8000-0000000000f1',
            channel_no: 6,
            channel_name: '空槽位',
            channel_group: '',
            enabled: true,
            version: 1,
            permissions: ['playback', 'configure'],
            configured: false,
          },
        ],
        next_cursor: null,
      } satisfies PageData<Schema<'Channel'>>
    if (path.startsWith('/api/v1/events?')) return { items: [], next_cursor: null }
    return {}
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const page = await view(client)
  await userEvent.click(page.getByLabelText('通道筛选').element())
  // The configured channel is offered; the empty slot is not.
  await expect.element(page.getByRole('option', { name: /CH05 大门/ })).toBeVisible()
  expect(page.getByRole('option', { name: /空槽位/ }).elements()).toHaveLength(0)
})

test('an event shows which rules it tripped and why', async () => {
  const { client } = fixture([
    {
      id: 'hit-1',
      event_id: eventID,
      rule_id: 'rule-1',
      rule_name: '门口进入',
      severity: 'high',
      zone_name: '门口',
      reason: { kind: 'enter', category: 'person', score: 0.82, zone: '门口' },
      false_positive: false,
      false_positive_note: '',
    },
  ])
  const screen = await view(client)
  await screen.getByTestId('event-card').first().click({ position: { x: 5, y: 5 } })
  await new Promise((r) => setTimeout(r, 400))
  await expect.element(screen.getByText('命中规则')).toBeVisible()
  const hit = screen.getByTestId('event-hit')
  await expect.element(hit).toHaveTextContent('门口进入')
  // The reason is the facts frozen at match time, so it still explains the
  // decision after the rule was edited.
  await expect.element(hit).toHaveTextContent(/区域进入/)
  await expect.element(hit).toHaveTextContent(/置信 82%/)
  await expect.element(hit).toHaveTextContent(/区域 门口/)
})

test('marking a hit as a false positive sends the judgement', async () => {
  const { client, mutations } = fixture([
    {
      id: 'hit-1',
      event_id: eventID,
      rule_id: 'rule-1',
      rule_name: '门口进入',
      severity: 'medium',
      zone_name: '',
      reason: { kind: 'appear', category: 'person', score: 0.6 },
      false_positive: false,
      false_positive_note: '',
    },
  ])
  const screen = await view(client)
  await screen.getByTestId('event-card').first().click({ position: { x: 5, y: 5 } })
  await screen.getByRole('button', { name: '标记规则命中为误报 门口进入' }).click()

  await vi.waitFor(() => expect(mutations.length).toBe(1))
  expect(mutations[0].path).toBe('/api/v1/event-hits/hit-1')
  expect(mutations[0].init.method).toBe('PATCH')
  expect(JSON.parse(String(mutations[0].init.body))).toMatchObject({ false_positive: true })
})

test('a hit already marked as a false positive can be cleared', async () => {
  const { client } = fixture([
    {
      id: 'hit-1',
      event_id: eventID,
      rule_id: 'rule-1',
      rule_name: '门口进入',
      severity: 'medium',
      zone_name: '',
      reason: { kind: 'appear', category: 'person', score: 0.6 },
      false_positive: true,
      false_positive_note: '是路灯',
    },
  ])
  const screen = await view(client)
  await screen.getByTestId('event-card').first().click({ position: { x: 5, y: 5 } })
  const hit = screen.getByTestId('event-hit')
  await expect.element(hit).toHaveTextContent('已标误报')
  await expect.element(hit).toHaveTextContent('备注：是路灯')
  await expect
    .element(screen.getByRole('button', { name: '标记规则命中非误报 门口进入' }))
    .toBeVisible()
})

// Gallery facts: channel pill, zone badge, the compact filter bar and the
// one-click handling shortcuts.
test('cards show channel and zone, and the filter bar counts and batch-marks', async () => {
  const recent = new Date(Date.now() - 60_000).toISOString()
  const events: Schema<'Event'>[] = [
    {
      id: eventID,
      channel_id: channelID,
      source: 'frigate',
      source_id: 'evt-1',
      category: 'person',
      score: 0.82,
      zones: ['门口'],
      started_at: recent,
      ended_at: new Date(Date.parse(recent) + 12_000).toISOString(),
      snapshot_state: 'gone',
      media_state: 'linked',
    },
    {
      id: '00000000-0000-4000-8000-0000000000e2',
      channel_id: channelID,
      source: 'frigate',
      source_id: 'evt-2',
      category: 'car',
      score: 0.71,
      zones: [],
      // No end and older than five minutes: the honest closed state.
      started_at: new Date(Date.now() - 3_600_000).toISOString(),
      ended_at: null,
      snapshot_state: 'pending',
      media_state: 'unavailable',
    },
  ]
  const { client } = fixture([], { events })
  const screen = await view(client)
  const cards = screen.getByTestId('event-card')
  await expect.element(cards).toHaveLength(2)
  // Channel identity and zone badge on every card.
  await expect.element(cards.nth(0)).toHaveTextContent('CH05 大门')
  await expect.element(cards.nth(0)).toHaveTextContent('门口')
  // The ended event carries the known duration; the ongoing recent one reads
  // 进行中.
  await expect.element(cards.nth(0)).toHaveTextContent('12s')
  await expect.element(cards.nth(1)).toHaveTextContent('结束未知')
  await expect.element(cards.nth(1)).toHaveTextContent('抓拍未到达')
  // The bar counts what has been FETCHED, not what exists: "已取 2 条" beside a
  // "还有更早的" hint never claims to be the whole index.
  await expect.element(screen.getByText(/已取 2 条/)).toBeVisible()
  // Handling was withdrawn from the product: no batch triage action, and no
  // worklist filter. What the card carries instead is the recording verdict.
  expect(screen.getByRole('button', { name: /全部标为已处理/ }).query()).toBeNull()
  expect(screen.getByLabelText('处理状态筛选').query()).toBeNull()
  await expect.element(cards.nth(0)).toHaveTextContent('已录像')
  await expect.element(cards.nth(1)).toHaveTextContent('未录像')
  client.clear()
})

test('an event with no upstream close says so instead of showing a bare dead tile', async () => {
  const stale: Schema<'Event'> = {
    id: eventID,
    channel_id: channelID,
    source: 'frigate',
    source_id: 'evt-1',
    category: 'person',
    score: 0.82,
    zones: ['门口'],
    // No end, one hour old: the end message was lost. It must not read as
    // 进行中 or 抓拍生成中 anymore.
    started_at: new Date(Date.now() - 3_600_000).toISOString(),
    ended_at: null,
    snapshot_state: 'pending',
    media_state: 'unavailable',
  }
  const { client } = fixture([], { events: [stale] })
  const screen = await view(client)
  const card = screen.getByTestId('event-card')
  await expect.element(card).toBeVisible()
  // Not "正在进行" and not a silent "已结束": the system knows it started and
  // does not know how it finished, and the pill says that.
  await expect.element(card).not.toHaveTextContent('进行中')
  await expect.element(card).not.toHaveTextContent('抓拍生成中')
  await expect.element(card).toHaveTextContent('结束未知')
  // The missing picture is a fault here, not an ordinary snapshotless
  // detection, and the card names the cause the operator can act on.
  await expect.element(card).toHaveTextContent('抓拍未到达')
  await expect.element(card).toHaveTextContent('事件未正常结束')
  await expect.element(card).not.toHaveTextContent('无抓拍图像')
  client.clear()
})

test('a closed event with no snapshot reads as an ordinary missing picture', async () => {
  const closed: Schema<'Event'> = {
    id: eventID,
    channel_id: channelID,
    source: 'frigate',
    source_id: 'evt-closed',
    category: 'person',
    score: 0.5,
    zones: [],
    started_at: new Date(Date.now() - 3_600_000).toISOString(),
    ended_at: new Date(Date.now() - 3_590_000).toISOString(),
    snapshot_state: 'gone',
    media_state: 'unavailable',
  }
  const { client } = fixture([], { events: [closed] })
  const screen = await view(client)
  const card = screen.getByTestId('event-card')
  // This one ended normally and simply has no picture: nothing is wrong, so it
  // must not be dressed up as a fault.
  await expect.element(card).toHaveTextContent('无抓拍图像')
  await expect.element(card).not.toHaveTextContent('抓拍未到达')
  await expect.element(card).not.toHaveTextContent('结束未知')
  client.clear()
})

// The card is the way into the footage, and the timestamp is the precise way in.
// Clicking the picture opens the drawer (that is what an operator points at);
// clicking the time jumps to that moment in playback.
test('the timestamp is the way into the footage, the card opens the drawer', async () => {
  const { client } = fixture([])
  const screen = await view(client)
  const card = screen.getByTestId('event-card')
  // The recording verdict sits where the worklist used to: top-right corner.
  await expect.element(card).toHaveTextContent('已录像')
  const replay = screen.getByRole('link', { name: /查看事件录像/ })
  await expect.element(replay).toBeVisible()
  // It is the timestamp itself, not a separate row under the card: one glyph
  // for "this moment", and clicking it is what "go there" means.
  await expect.element(replay).toHaveTextContent(/\d\d:\d\d:\d\d/)
  // Nothing on the card triages the event any more — that whole vocabulary is
  // gone from the product.
  expect(card.getByRole('button', { name: '标为已处理' }).query()).toBeNull()
  client.clear()
})

test('the drawer says what happened to the alert, including a refusal', async () => {
  const { client } = fixture([], {
    deliveries: [
      {
        channel_id: 'chan-1',
        channel_name: '站内提醒',
        channel_kind: 'inapp',
        notification_id: 'n-1',
        state: 'succeeded',
        updated_at: '2026-10-13T12:00:05Z',
        title: 'CH05 大门 人 中',
        severity: 'medium',
      },
      {
        channel_id: 'chan-2',
        channel_name: '值班群',
        channel_kind: 'webhook',
        notification_id: 'n-1',
        state: 'exhausted',
        error: 'receiver answered 401: unauthorized',
        http_status: 401,
        updated_at: '2026-10-13T12:01:00Z',
        title: 'CH05 大门 人 中',
        severity: 'medium',
      },
    ],
  })
  const screen = await view(client)
  await screen.getByTestId('event-card').click()
  // Exact: the section's own help text mentions the same channel name.
  await expect.element(screen.getByText('站内提醒', { exact: true })).toBeVisible()
  await expect.element(screen.getByText('成功', { exact: true })).toBeVisible()
  // A refusal must name the status, not just say a channel failed: that is the
  // only actionable evidence a webhook produces.
  await expect.element(screen.getByText('最终失败', { exact: true })).toBeVisible()
  await expect.element(screen.getByText('HTTP 401')).toBeVisible()
  await expect.element(screen.getByText(/receiver answered 401/)).toBeVisible()
})

test('an event nobody was told about says so instead of showing an empty box', async () => {
  const { client } = fixture([])
  const screen = await view(client)
  await screen.getByTestId('event-card').click()
  await expect
    .element(screen.getByText(/没有触发通知：可能是规则未绑定渠道/))
    .toBeVisible()
})

test('a closed event waiting for its picture says so, not "no picture"', async () => {
  // The detector announces snapshots as they stand when an event ends, so one
  // written just after is never announced. Until reconciliation asks, this is
  // neither "loading" nor "missing" — it is "we will check".
  const pending: Schema<'Event'> = {
    id: eventID,
    channel_id: channelID,
    source: 'frigate',
    source_id: 'evt-pending',
    category: 'person',
    score: 0.74,
    zones: [],
    started_at: new Date(Date.now() - 3_600_000).toISOString(),
    ended_at: new Date(Date.now() - 3_540_000).toISOString(),
    snapshot_state: 'pending',
    media_state: 'linked',
  }
  const { client } = fixture([], { events: [pending] })
  const screen = await view(client)
  const card = screen.getByTestId('event-card')
  await expect.element(card).toHaveTextContent('抓拍待同步')
  await expect.element(card).toHaveTextContent('稍后自动核对')
  // Not dressed as a fault (nothing is known to be wrong) and not as an
  // ordinary missing picture (which would be a claim it does not have).
  await expect.element(card).not.toHaveTextContent('无抓拍图像')
  await expect.element(card).not.toHaveTextContent('抓拍未到达')
  client.clear()
})

// REC-09: the event page must say what footage exists instead of offering a
// play button that opens an empty range.
//
// Each case builds its own event with a *recent* timestamp: the page filters to
// a 7-day window, so the fixture's fixed 2026-10-13 event would be dropped and
// the assertion would pass against an empty page.
function mediaEvent(overrides: Partial<Schema<'Event'>>): Schema<'Event'> {
  const started = new Date(Date.now() - 60_000).toISOString()
  return {
    id: eventID,
    channel_id: channelID,
    source: 'frigate',
    source_id: 'evt-media',
    category: 'person',
    score: 0.82,
    zones: ['门口'],
    started_at: started,
    ended_at: new Date(Date.parse(started) + 12_000).toISOString(),
    snapshot_state: 'gone',
    media_state: 'linked',
    ...overrides,
  }
}

test('an event without footage is not a link and says 未录像', async () => {
  const { client } = fixture([], {
    events: [mediaEvent({ media_state: 'unavailable' })],
  })
  const screen = await view(client)
  // The verdict is on the card, not a second row underneath it: the two lines
  // ("本机无录像 · 可复制时间人工查询") repeated what the tile already showed.
  await expect.element(screen.getByTestId('event-card')).toHaveTextContent('未录像')
  await expect
    .element(screen.getByRole('link', { name: /查看事件录像/ }))
    .not.toBeInTheDocument()
  await expect.element(screen.getByText('本机无录像')).not.toBeInTheDocument()
  client.clear()
})

// A linked event seeks to the available span, not to "start minus ten seconds":
// the segment boundary lands where the keyframe put it, and seeking into a gap
// produces an empty player that reads as a broken recording.
test('a linked event seeks to the available start', async () => {
  const available = new Date(Date.now() - 65_000).toISOString()
  const { client } = fixture([], {
    events: [mediaEvent({ media_state: 'linked', media_start_at: available })],
  })
  const screen = await view(client)
  await expect
    .element(screen.getByRole('link', { name: /查看事件录像/ }))
    .toBeVisible()
  client.clear()
})

// Partial coverage keeps the link — footage exists — but states the limit, so
// the operator is not surprised when playback stops early.
test('a partially covered event keeps playback and says so in the badge', async () => {
  const { client } = fixture([], {
    events: [mediaEvent({ media_state: 'partial' })],
  })
  const screen = await view(client)
  await expect
    .element(screen.getByRole('link', { name: /查看事件录像/ }))
    .toBeVisible()
  await expect.element(screen.getByTestId('event-card')).toHaveTextContent('部分录像')
  client.clear()
})

// An event that is still running has a clip that is still being extended, and its
// segment is not published yet. Offering playback would open a file that grows
// underneath the player, and the last seconds an operator most wants are exactly
// the ones not written. The page must say 录像生成中 and must not link.
test('a running event says its recording is still being produced', async () => {
  const { client } = fixture([], {
    events: [mediaEvent({ media_state: 'linked', ended_at: undefined })],
  })
  const screen = await view(client)
  // A running event's segment is not published yet, so the card must not claim
  // 已录像 and its timestamp must not link: the player would open a file that
  // grows underneath it.
  await expect.element(screen.getByTestId('event-card')).toHaveTextContent('录像生成中')
  await expect.element(screen.getByTestId('event-card')).not.toHaveTextContent('已录像')
  await expect
    .element(screen.getByRole('link', { name: /查看事件录像/ }))
    .not.toBeInTheDocument()
  client.clear()
})

// A one-second detection never gets a snapshot: the detector writes them while an
// event runs, so it can end before the first frame lands. Saying "无抓拍图像"
// would read as a lost picture and send the operator looking for a fault that
// does not exist.
test('a very short event says it was too short rather than missing its picture', async () => {
  const started = new Date(Date.now() - 60_000).toISOString()
  const { client } = fixture([], {
    events: [
      mediaEvent({
        started_at: started,
        ended_at: new Date(Date.parse(started) + 1_000).toISOString(),
        snapshot_state: 'gone',
      }),
    ],
  })
  const screen = await view(client)
  await expect.element(screen.getByText(/事件过短，未及抓拍/)).toBeVisible()
  await expect.element(screen.getByText(/持续 1 秒/)).toBeVisible()
  await expect
    .element(screen.getByText('无抓拍图像'))
    .not.toBeInTheDocument()
  client.clear()
})

// A longer event with no picture is a genuine absence and keeps the old wording:
// excusing it would hide a real fault.
test('a longer event without a picture keeps the plain wording', async () => {
  const started = new Date(Date.now() - 60_000).toISOString()
  const { client } = fixture([], {
    events: [
      mediaEvent({
        started_at: started,
        ended_at: new Date(Date.parse(started) + 30_000).toISOString(),
        snapshot_state: 'gone',
      }),
    ],
  })
  const screen = await view(client)
  await expect.element(screen.getByText('无抓拍图像')).toBeVisible()
  await expect
    .element(screen.getByText(/事件过短/))
    .not.toBeInTheDocument()
  client.clear()
})
