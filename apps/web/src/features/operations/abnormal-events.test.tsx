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
import { ApiError } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { AbnormalEvents } from './abnormal-events'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => vi.clearAllMocks())

function event(overrides: Partial<Schema<'AbnormalEvent'>> = {}): Schema<'AbnormalEvent'> {
  return {
    id: '00000000-0000-4000-8000-0000000000a1',
    kind: 'detector_unavailable',
    severity: 'critical',
    subject: 'frigate',
    state: 'firing',
    summary: '检测服务不可用',
    detail: 'Frigate 无响应',
    occurrences: 1,
    started_at: new Date(Date.now() - 60_000).toISOString(),
    last_seen_at: new Date().toISOString(),
    ...overrides,
  }
}

function fixture(options: { events?: Schema<'AbnormalEvent'>[]; fail?: boolean } = {}) {
  calls.request.mockImplementation(async (path: string) => {
    if (options.fail) throw new ApiError(503, 'service_error', '不可用')
    if (path.startsWith('/api/v1/operations/abnormal-events')) {
      return { items: options.events ?? [] }
    }
    throw new Error('Unexpected query ' + path)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const route = createRootRoute({
    component: () => (
      <QueryClientProvider client={client}>
        <AbnormalEvents zone='Asia/Shanghai' />
      </QueryClientProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return { client, view: <RouterProvider router={router} /> }
}

// EVT-04: the page must answer "what is broken now". An empty list is a real
// answer and says so, rather than leaving the panel blank.
test('a healthy site says there are no abnormal events', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await expect.element(view.getByText('当前没有异常事件')).toBeVisible()
  setup.client.clear()
})

// A condition seen repeatedly shows its count: a row that keeps reappearing is a
// flapping fault, not a new one, and the operator needs to see that.
test('a flapping condition shows how often it was seen', async () => {
  const setup = fixture({ events: [event({ occurrences: 7 })] })
  const view = await render(setup.view)
  await expect.element(view.getByText(/反复 7 次/)).toBeVisible()
  setup.client.clear()
})

// Resolved incidents are hidden by default but reachable: "it came back" is
// information an operator needs the next morning.
test('resolved incidents appear when asked for', async () => {
  const resolved = event({
    id: '00000000-0000-4000-8000-0000000000a2',
    state: 'resolved',
    severity: 'warning',
    summary: '配置发布失败',
    ended_at: new Date().toISOString(),
  })
  const setup = fixture({ events: [resolved] })
  const view = await render(setup.view)
  // Hidden while the toggle is off: only firing work is shown by default.
  await expect.element(view.getByText('当前没有异常事件')).toBeVisible()
  await userEvent.click(view.getByRole('button', { name: /含已恢复/ }))
  await expect.element(view.getByTestId('abnormal-event')).toBeVisible()
  await expect
    .element(view.getByTestId('abnormal-event').getByText('已恢复'))
    .toBeVisible()
  setup.client.clear()
})

// A failed read hides the panel rather than showing "0 异常": claiming the site is
// healthy when the evidence is missing is the one thing this panel must not do.
test('an unreadable incident list renders nothing', async () => {
  const setup = fixture({ fail: true })
  const view = await render(setup.view)
  await expect
    .element(view.getByText('当前没有异常事件'))
    .not.toBeInTheDocument()
  setup.client.clear()
})
