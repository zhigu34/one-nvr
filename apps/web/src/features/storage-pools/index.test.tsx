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
    warning_percent: 80,
    pressure_percent: 85,
    pressure_target_percent: 80,
    critical_percent: 95,
    ...overrides,
  }
}

function fixture(
  pools: Schema<'Pool'>[] = [poolFixture()],
  retentionDays = 0
) {
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
    // The binding panel reads the channel list and its per-channel summary.
    if (path === '/api/v1/channels?limit=100')
      return {
        items: [
          {
            id: '00000000-0000-4000-8000-000000000001',
            channel_no: 1,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 3,
            permissions: ['configure'],
            configured: true,
          },
        ],
        next_cursor: null,
      }
    if (path === '/api/v1/channels/summary')
      return {
        items: [
          {
            channel_id: '00000000-0000-4000-8000-000000000001',
            channel_no: 1,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 3,
            permissions: ['configure'],
                        current_revision_id: 'rev',
            storage_pool_id: null,
            recording_mode: 'none',
            source_ip: '192.168.66.110',
            main: { state: 'healthy', reason: '', observed_at: null, expires_at: null },
            sub: { state: 'unknown', reason: 'observation_missing', observed_at: null, expires_at: null },
            recording: { state: 'disabled', reason: 'recording_disabled', observed_at: null, expires_at: null },
            last_error: '',
            updated_at: null,
          },
        ],
      }
    if (path.endsWith('/source/status'))
      return {
        channel_id: '00000000-0000-4000-8000-000000000001',
        current_revision_id: 'rev',
        desired_revision_id: 'rev',
        storage_pool_id: null,
        enabled: true,
        version: 3,
        main: { state: 'healthy', reason: '' },
        sub: { state: 'unhealthy', reason: '' },
        recording: { state: 'disabled', reason: 'recording_disabled' },
        requires_initial_recording_mode: false,
      }
    if (init?.method === 'PUT' && path.endsWith('/storage-pool'))
      return { job_id: 'job-bind', state: 'queued' }
    // The retention panel lives here now but its scope is the site.
    if (path === '/api/v1/site')
      return {
        id: 'site',
        name: 'Test site',
        timezone: 'Asia/Shanghai',
        channel_count: 16,
        require_storage_write_proof: false,
        recording_retention_days: retentionDays,
        version: 1,
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

// Binding moved here from the recording plan: the pool card names its channels
// and offers the multi-select panel. The writes are the same per-channel PUT.
test('the pool card lists bound channels and binds a picked channel', async () => {
  const client = fixture()
  const screen = await render(view(client))
  // The summary line renders before any panel is opened.
  await expect
    .element(screen.getByText('未绑定通道'))
    .toBeVisible()
  await screen.getByRole('button', { name: '管理绑定通道' }).click()
  const pick = screen.getByRole('button', { name: '绑定 CH01 大门' })
  await expect.element(pick).toBeVisible()
  await pick.click()
  const bind = screen.getByRole('button', { name: /绑定所选/ })
  await expect.element(bind).toBeVisible()
  await bind.click()
  await vi.waitFor(() => {
    const put = calls.request.mock.calls.find(
      ([path, init]) => init?.method === 'PUT' && String(path).endsWith('/storage-pool')
    )
    expect(put).toBeTruthy()
    expect(JSON.parse((put?.[1] as RequestInit).body as string)).toEqual({
      pool_id: '00000000-0000-4000-8000-000000000001',
    })
  })
  await expect.element(screen.getByText(/1 路已绑定 pool-1/)).toBeVisible()
  client.clear()
})

// The read-only summary names the channels a pool already serves, without
// opening anything.
test('a pool with bound channels says so in one line', async () => {
  const client = fixture([poolFixture()])
  // Point the summary at the pool the fixture binds: CH01 already on pool-1.
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    void init
    if (path.startsWith('/api/v1/storage-pools?'))
      return {
        items: [poolFixture()],
        next_cursor: null,
        capacity: {
          filesystem_count: 1,
          total_bytes: 401.6 * GiB,
          free_bytes: 301.3 * GiB,
        },
      } as unknown as PageData<Schema<'Pool'>> & {
        capacity: Schema<'StorageCapacity'>
      }
    if (path === '/api/v1/channels/summary')
      return {
        items: [
          {
            channel_id: '00000000-0000-4000-8000-000000000001',
            channel_no: 1,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 3,
            permissions: ['configure'],
                        current_revision_id: 'rev',
            storage_pool_id: '00000000-0000-4000-8000-000000000001',
            recording_mode: 'none',
            source_ip: '192.168.66.110',
            main: { state: 'healthy', reason: '', observed_at: null, expires_at: null },
            sub: { state: 'unknown', reason: 'observation_missing', observed_at: null, expires_at: null },
            recording: { state: 'disabled', reason: 'recording_disabled', observed_at: null, expires_at: null },
            last_error: '',
            updated_at: null,
          },
        ],
      }
    if (path === '/api/v1/channels?limit=100') return { items: [], next_cursor: null }
    throw new Error('unexpected path ' + path)
  })
  const screen = await render(view(client))
  await expect
    .element(screen.getByText('已绑定 1 路通道：CH01'))
    .toBeVisible()
  client.clear()
})

// Local retention is a SITE policy: one period covers every channel whichever
// pool it is bound to. It sits on the storage page because that is where an
// operator decides how long footage is kept, so the panel must say the scope
// rather than let the neighbouring per-pool cards imply otherwise.
test('the retention policy states its site-wide scope and saves a period', async () => {
  const client = fixture([poolFixture()], 0)
  const screen = await render(view(client))
  await expect
    .element(screen.getByText(/当前不自动删除任何录像/))
    .toBeVisible()
  await expect
    .element(screen.getByText(/本站点全部通道.*与通道绑定哪个存储池无关/))
    .toBeVisible()
  await screen.getByLabelText('保留天数').click()
  await screen.getByRole('option', { name: '7 天', exact: true }).click()
  await screen.getByRole('button', { name: '保存保留策略' }).click()
  const saves = calls.request.mock.calls.filter(
    ([path, init]) =>
      path === '/api/v1/site' && (init as RequestInit)?.method === 'PATCH'
  )
  expect(saves).toHaveLength(1)
  expect(JSON.parse(String((saves[0][1] as RequestInit).body))).toEqual({
    recording_retention_days: 7,
  })
})

test('an enabled retention period says it deletes and that protection is skipped', async () => {
  const client = fixture([poolFixture()], 30)
  const screen = await render(view(client))
  await expect
    .element(screen.getByText(/超过 30 天的录像会自动删除本地副本/))
    .toBeVisible()
  await expect.element(screen.getByText(/已保护的录像始终跳过/)).toBeVisible()
})
