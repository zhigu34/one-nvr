import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
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

function fixture(hits: Schema<'RuleHit'>[]) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  const mutations: { path: string; init: RequestInit }[] = []
  const event: Schema<'Event'> = {
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
    media_state: 'linked',
    handle_state: 'open',
    version: 3,
    handling: [],
  }
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
          },
        ],
        next_cursor: null,
      } satisfies PageData<Schema<'Channel'>>
    if (path.startsWith('/api/v1/events?'))
      return { items: [event], next_cursor: null }
    if (path === `/api/v1/events/${eventID}`) return { ...event, hits }
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
  await screen.getByTestId('event-card').first().click()
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
  await screen.getByTestId('event-card').first().click()
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
  await screen.getByTestId('event-card').first().click()
  const hit = screen.getByTestId('event-hit')
  await expect.element(hit).toHaveTextContent('已标误报')
  await expect.element(hit).toHaveTextContent('备注：是路灯')
  await expect
    .element(screen.getByRole('button', { name: '标记规则命中非误报 门口进入' }))
    .toBeVisible()
})
