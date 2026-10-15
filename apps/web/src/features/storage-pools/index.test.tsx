import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import type { PageData, Schema } from '@/lib/types'
import { StoragePools } from './index'
// The test browser does not load main.tsx, so no styles arrive by default;
// importing the app CSS here keeps the width assertions on the capacity bar
// and the add-form button honest instead of measuring unstyled elements.
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

const GiB = 1024 * 1024 * 1024

function poolFixture(
  overrides: Partial<Schema<'Pool'>> & { checks?: Schema<'PoolCheck'>[] } = {}
): Schema<'Pool'> {
  return {
    id: '00000000-0000-4000-8000-000000000001',
    name: 'pool-1',
    path: '/storage/pool-1',
    enabled: true,
    is_default: true,
    version: 1,
    checks:
      overrides.checks ??
      ([
        {
          service: 'api',
          state: 'healthy',
          observed_at: null,
          reason: 'read_write_delete_verified',
          total_bytes: 401.6 * GiB,
          free_bytes: 371 * GiB,
        },
        {
          service: 'worker',
          state: 'healthy',
          observed_at: null,
          reason: 'read_write_delete_verified',
          total_bytes: 401.6 * GiB,
          free_bytes: 371 * GiB,
        },
        {
          service: 'zlm',
          state: 'expired',
          observed_at: null,
          reason: 'observation_expired',
          total_bytes: 401.6 * GiB,
          free_bytes: 371 * GiB,
        },
      ] as Schema<'PoolCheck'>[]),
    state: 'pending',
    used_bytes: 30.6 * GiB,
    ...overrides,
  }
}

function fixture(pools: Schema<'Pool'>[] = [poolFixture()]) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method === 'POST' && path === '/api/v1/storage-pools')
      return { id: 'new' }
    if (path.startsWith('/api/v1/storage-pools?'))
      return {
        items: pools,
        next_cursor: null,
        capacity: {
          filesystem_count: 1,
          total_bytes: 401.6 * GiB,
          free_bytes: 301.3 * GiB,
        },
      } as unknown as PageData<Schema<'Pool'>> & {
        capacity: Schema<'StorageCapacity'>
      }
    throw new Error('unexpected path ' + path)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return client
}

const view = (client: QueryClient) => (
  <QueryClientProvider client={client}>
    <StoragePools />
  </QueryClientProvider>
)

test('the add form stays collapsed until asked and packs one row', async () => {
  const client = fixture()
  const screen = await render(view(client))
  // Collapsed by default: no form on first paint.
  expect(screen.getByLabelText('存储池名称').query()).toBeNull()
  await screen
    .getByRole('button', { name: /添加存储池/ })
    .click()
  await expect.element(screen.getByLabelText('存储池名称')).toBeVisible()
  await expect.element(screen.getByLabelText('挂载目录')).toBeVisible()
  // The submit is a compact button, not a stretched column.
  const submit = screen.getByRole('button', { name: '确认添加' })
  await expect.element(submit).toBeVisible()
  const box = (submit.element() as HTMLElement).getBoundingClientRect()
  expect(box.width).toBeLessThan(200)
  await userEvent.fill(screen.getByLabelText('存储池名称'), '验证池')
  await userEvent.fill(screen.getByLabelText('挂载目录'), '/storage/pool1')
  await submit.click()
  await vi.waitFor(() =>
    expect(
      calls.request.mock.calls.filter(
        ([path, init]) => path === '/api/v1/storage-pools' && init?.method === 'POST'
      ).length
    ).toBe(1)
  )
  client.clear()
})

test('the pool card header is one aligned row with badges and actions', async () => {
  const client = fixture()
  const screen = await render(view(client))
  const header = screen.getByText('pool-1', { exact: true })
  await expect.element(header).toBeVisible()
  // Path as a code badge, default and enabled pills on the same row.
  await expect.element(screen.getByText('/storage/pool-1')).toBeVisible()
  await expect.element(screen.getByText('默认池')).toBeVisible()
  await expect.element(screen.getByText('已启用')).toBeVisible()
  await expect.element(screen.getByText('待检查')).toBeVisible()
  // The e2e relies on this exact button name.
  await expect
    .element(screen.getByRole('button', { name: /立即检查/ }))
    .toBeVisible()
  // Settings stay collapsed until the toggle is pressed.
  expect(screen.getByLabelText('名称', { exact: true }).query()).toBeNull()
  await screen.getByRole('button', { name: /存储池设置/ }).click()
  await expect.element(screen.getByLabelText('名称', { exact: true })).toBeVisible()
  // The destructive action sits at the far end of the settings row.
  const remove = screen.getByRole('button', { name: '移除登记' })
  await expect.element(remove).toBeVisible()
  const box = (remove.element() as HTMLElement).getBoundingClientRect()
  const save = screen.getByRole('button', { name: '保存设置' }).element() as HTMLElement
  expect(box.left).toBeGreaterThan(save.getBoundingClientRect().left)
  client.clear()
})

test('the segmented capacity bar splits business, other and free space', async () => {
  const client = fixture()
  const screen = await render(view(client))
  const bar = screen.getByTestId('capacity-bar')
  await expect.element(bar).toBeVisible()
  // Business bytes in emerald; the dimmed "other" segment appears only when
  // the filesystem holds more than our indexed recordings.
  const used = bar.element().querySelector('[data-testid="capacity-used"]') as HTMLElement
  expect(used).toBeTruthy()
  // 30.6 GiB of 401.6 GiB ≈ 7.6%; free 371 GiB leaves no "other" segment.
  expect(parseFloat(used.style.width)).toBeCloseTo(7.6, 0)
  expect(
    bar.element().querySelector('[data-testid="capacity-other"]')
  ).toBeNull()
  await expect.element(screen.getByText(/业务录像占用/)).toBeVisible()
  // The card's own probe numbers (free 371), not the page-level aggregate.
  await expect
    .element(screen.getByText(/可用 371\.0 GiB \/ 共 401\.6 GiB \(已用 8%\)/))
    .toBeVisible()
  client.clear()
})

test('check facts live inside their probe cards, not in a bare list', async () => {
  const client = fixture()
  const screen = await render(view(client))
  await screen.getByText('检查详情').click()
  const api = screen.getByTestId('pool-check-api')
  await expect.element(api).toBeVisible()
  await expect.element(api.getByText('目录访问')).toBeVisible()
  await expect.element(api.getByText('正常 (检查通过)')).toBeVisible()
  // The mono probe line is embedded in the same card.
  await expect.element(api.getByText('api · read_write_delete_verified')).toBeVisible()
  const zlm = screen.getByTestId('pool-check-zlm')
  await expect.element(zlm.getByText('检查已过期')).toBeVisible()
  await expect.element(zlm.getByText('请重新检查或查看诊断')).toBeVisible()
  await expect.element(zlm.getByText('zlm · observation_expired')).toBeVisible()
  // The old standalone English list is gone: every service code appears
  // exactly once per card footer, never repeated below the grid.
  const text = document.body.textContent || ''
  expect(text.split('read_write_delete_verified').length - 1).toBe(2)
  client.clear()
})
