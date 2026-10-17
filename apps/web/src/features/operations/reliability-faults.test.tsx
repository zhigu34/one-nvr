import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { useAuthStore } from '@/stores/auth-store'
import { ReliabilityOperations } from './reliability'
import '@/styles/index.css'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})

type Fault = Record<string, unknown>

function fault(overrides: Fault = {}): Fault {
  return {
    id: 'f1',
    channel_id: 'ch-8',
    channel_no: 8,
    kind: 'sub',
    state: 'resolved',
    started_at: '2026-10-17T10:00:00Z',
    ended_at: '2026-10-17T10:00:10Z',
    duration_ms: 10_000,
    initial_reason: 'sub_source_unavailable',
    category: 'unknown',
    effective_category: 'unknown',
    note: '',
    maintenance: false,
    ...overrides,
  }
}

function setup(faults: Fault[]) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  calls.request.mockImplementation(async (path: string) => {
    if (path.startsWith('/api/v1/reliability/summary'))
      return { items: [] }
    if (path.startsWith('/api/v1/reliability/alerts'))
      return { items: [], kinds: [] }
    if (path.startsWith('/api/v1/faults'))
      return { items: faults, categories: [] }
    if (path.startsWith('/api/v1/reliability/alert-rules'))
      return { items: [], kinds: [] }
    if (path.startsWith('/api/v1/channels/summary'))
      return { items: [] }
    return {}
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <ReliabilityOperations />
    </QueryClientProvider>
  )
}

// A flapping sub stream plus one long main-stream outage: the case the filters
// and the aggregation exist for.
const MIXED: Fault[] = [
  fault({ id: 'a1', started_at: '2026-10-17T10:00:00Z', ended_at: '2026-10-17T10:00:10Z' }),
  fault({ id: 'a2', started_at: '2026-10-17T10:01:00Z', ended_at: '2026-10-17T10:01:10Z' }),
  fault({ id: 'a3', started_at: '2026-10-17T10:02:00Z', ended_at: '2026-10-17T10:02:10Z' }),
  fault({
    id: 'b1',
    channel_id: 'ch-5',
    channel_no: 5,
    kind: 'main',
    started_at: '2026-10-17T08:00:00Z',
    ended_at: '2026-10-17T08:09:00Z',
    duration_ms: 540_000,
    effective_category: 'upstream_unavailable',
  }),
]

test('reports how many rows the filter kept out of the window total', async () => {
  const page = await setup(MIXED)
  // Everything, aggregated: three flapping entries collapse into one group.
  await expect.element(page.getByText(/共 4 条/)).toBeVisible()
  await expect.element(page.getByText(/· 2 组/)).toBeVisible()
})

test('hides sub-stream blips with the duration floor', async () => {
  const page = await setup(MIXED)
  await page.getByRole('combobox', { name: /最短时长/ }).click()
  await page.getByRole('option', { name: '≥ 5 分钟' }).click()
  // One nine-minute outage survives; the three ten-second drops do not.
  await expect.element(page.getByText('1 / 4 条')).toBeVisible()
  await expect.element(page.getByText(/9 分/)).toBeVisible()
})

test('filters by channel so a noisy test channel can be excluded', async () => {
  const page = await setup(MIXED)
  await page.getByRole('combobox', { name: /^通道/ }).click()
  await page.getByRole('option', { name: 'CH05' }).click()
  await expect.element(page.getByText('1 / 4 条')).toBeVisible()
})

test('switching aggregation off shows every fault as its own row', async () => {
  const page = await setup(MIXED)
  await page.getByRole('switch', { name: '聚合连续抖动' }).click()
  await expect.element(page.getByText(/共 4 条/)).toBeVisible()
  // No group count suffix once aggregation is off.
  expect(page.getByText(/· \d+ 组/).query()).toBeNull()
})

test('a filtered-empty list says so instead of looking like a quiet window', async () => {
  const page = await setup([fault({ duration_ms: 20_000 })])
  await page.getByRole('combobox', { name: /最短时长/ }).click()
  await page.getByRole('option', { name: '≥ 5 分钟' }).click()
  await expect
    .element(page.getByText('当前筛选条件下没有故障。'))
    .toBeVisible()
})
