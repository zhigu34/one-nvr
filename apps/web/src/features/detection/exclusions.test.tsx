import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import type { Schema } from '@/lib/types'
import { ExclusionEditor } from './exclusions'
import type { Point } from './zones'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))

afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})

/**
 * The canvas needs a detection frame to draw on, and the test browser has no
 * camera. This stand-in forwards the props the editor drives so the submit path
 * can be exercised without a media pipeline.
 */
vi.mock('./zones', () => ({
  ZoneCanvas: () => null,
}))

function harness(rules: Schema<'DetectionExclusion'>[], draft: Point[]) {
  useAuthStore.getState().setUser({
    id: 'a', username: 'admin', role: 'admin', enabled: true, version: 1,
  })
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (method === 'GET' && path.includes('/detection/exclusions'))
      return { items: rules }
    if (method === 'POST' || method === 'DELETE') return {}
    throw new Error(`unexpected ${method} ${path}`)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <ExclusionEditor channelID='c1' detectionEnabled initialDraft={draft} />
    </QueryClientProvider>
  )
}

test('a saved rule shows its window and how much it suppressed', async () => {
  const view = await harness(
    [
      {
        id: 'r1',
        channel_id: 'c1',
        name: '夜间门前停车位',
        coordinates: [[0, 0], [0.5, 0], [0.5, 1]],
        categories: ['person'],
        from_clock: '22:00',
        to_clock: '07:00',
        reason: '夜间误判电动车',
        enabled: true,
        version: 1,
        suppressed_24h: 137,
      },
    ],
    []
  )
  const row = view.getByTestId('exclusion-row')
  await expect.element(row).toBeVisible()
  await expect.element(row).toHaveTextContent('夜间门前停车位')
  await expect.element(row).toHaveTextContent('22:00–07:00')
  // The count is the point: a rule eating 137 events must be visible as such,
  // because otherwise the page looks identical to a quiet site.
  await expect.element(view.getByTestId('exclusion-suppressed')).toHaveTextContent('24h 排除 137')
})

test('saving a drawn region posts the polygon, window and categories', async () => {
  const view = await harness([], [
    { x: 0.1, y: 0.2 },
    { x: 0.6, y: 0.2 },
    { x: 0.6, y: 0.8 },
  ])
  await userEvent.fill(view.getByLabelText('排除规则名称'), '门前电动车')
  await userEvent.fill(view.getByLabelText('排除类别'), 'person')
  await view.getByRole('button', { name: /保存排除规则/ }).click()
  await vi.waitFor(() =>
    expect(calls.request.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(true)
  )
  const [, init] = calls.request.mock.calls.find(([, value]) => value?.method === 'POST')!
  const body = JSON.parse(String(init.body))
  expect(body.name).toBe('门前电动车')
  expect(body.coordinates).toEqual([[0.1, 0.2], [0.6, 0.2], [0.6, 0.8]])
  expect(body.from_clock).toBe('22:00')
  expect(body.to_clock).toBe('07:00')
  expect(body.categories).toEqual(['person'])
})

test('a rule with fewer than three points cannot be saved', async () => {
  const view = await harness([], [{ x: 0.1, y: 0.2 }, { x: 0.6, y: 0.2 }])
  await expect
    .element(view.getByRole('button', { name: /保存排除规则/ }))
    .toBeDisabled()
})
