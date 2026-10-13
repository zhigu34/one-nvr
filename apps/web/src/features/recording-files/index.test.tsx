import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import type { Schema } from '@/lib/types'
import { RecordingFiles } from './index'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})

const CH = '00000000-0000-4000-8000-000000000001'
const OLD = '00000000-0000-4000-8000-0000000000a1'
const PROTECTED = '00000000-0000-4000-8000-0000000000a2'
const GONE = '00000000-0000-4000-8000-0000000000a3'

function segment(
  id: string,
  override: Partial<Schema<'RecordingSegment'>> = {}
): Schema<'RecordingSegment'> {
  return {
    id,
    channel_id: CH,
    source_revision_id: '00000000-0000-4000-8000-0000000000b1',
    pool_id: '00000000-0000-4000-8000-0000000000c1',
    run_id: '00000000-0000-4000-8000-0000000000d1',
    start: '2026-10-04T02:00:00Z',
    end: '2026-10-04T02:01:00Z',
    bytes: 1024 * 1024,
    state: 'ready',
    naming_timezone: 'Asia/Shanghai',
    utc_offset_seconds: 28800,
    check_state: 'ready',
    protected: false,
    media: { codec: 'h264', width: 1920, height: 1080, fps: 25, duration_ms: 60000 },
    ...override,
  }
}

function fixture(
  options: { retentionDays?: number; outcomes?: Schema<'RecordingDeleteOutcome'>[] } = {}
) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  const mutations: { path: string; body: unknown }[] = []
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method && init.method !== 'GET') {
      mutations.push({
        path,
        body: init.body ? JSON.parse(init.body as string) : undefined,
      })
      if (path.endsWith('/batch-delete-local')) {
        const items = options.outcomes || [
          { segment_id: OLD, state: 'deleted', bytes: 1024 * 1024 },
        ]
        const released = items.reduce((sum, item) => sum + (item.bytes || 0), 0)
        return { items, released_bytes: released }
      }
      if (path === '/api/v1/site') return { ...site(options.retentionDays ?? 0) }
      return segment(OLD)
    }
    if (path === '/api/v1/channels?limit=100')
      return {
        items: [
          {
            id: CH,
            channel_no: 1,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 1,
            permissions: ['playback', 'configure'],
          },
        ],
        next_cursor: null,
      }
    if (path === '/api/v1/site') return site(options.retentionDays ?? 0)
    if (path.startsWith('/api/v1/recordings/stats'))
      return {
        segments: 3,
        ready: 1,
        anomalous: 1,
        protected: 1,
        deleted: 0,
        duration_ms: 180000,
        logical_bytes: 3 * 1024 * 1024,
        deleted_bytes: 0,
        oldest_start: '2026-10-04T02:00:00Z',
        newest_end: '2026-10-04T03:01:00Z',
      }
    if (path.startsWith('/api/v1/recordings?'))
      return {
        items: [
          segment(OLD),
          segment(PROTECTED, { protected: true }),
          segment(GONE, { state: 'missing', check_state: 'missing' }),
        ],
        next_cursor: null,
      }
    if (path.includes('/source-revisions'))
      return {
        items: [
          {
            id: '00000000-0000-4000-8000-0000000000b1',
            channel_id: CH,
            source_id: '00000000-0000-4000-8000-0000000000e1',
            number: 3,
            config: {
              ip: '192.168.33.20',
              rtsp_port: 554,
              main_path: '/main',
              sub_path: '',
              transport: 'tcp',
            },
            username_summary: 'a***',
            password_state: 'saved',
            created_at: '2026-10-04T00:00:00Z',
          },
        ],
        next_cursor: null,
      }
    if (path.startsWith('/api/v1/storage-pools'))
      return { items: [], next_cursor: null }
    throw new Error('Unexpected query ' + path)
  })
  return {
    mutations,
    view: (
      <QueryClientProvider client={new QueryClient()}>
        <RecordingFiles initialChannel={CH} />
      </QueryClientProvider>
    ),
  }
}

function site(retentionDays: number): Schema<'Site'> {
  return {
    id: '00000000-0000-4000-8000-0000000000f1',
    name: '站点',
    timezone: 'Asia/Shanghai',
    channel_count: 16,
    require_storage_write_proof: true,
    recording_retention_days: retentionDays,
    version: 4,
  }
}

test('lists recordings with media facts and states, and marks deleted rows unselectable', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  await expect.element(view.getByTestId('recording-row')).toHaveLength(3)
  const first = view.getByTestId('recording-row').nth(0)
  await expect.element(first).toHaveTextContent('1920×1080 · h264')
  await expect.element(first).toHaveTextContent('可用')
  // A file that vanished outside one-nvr reads as missing, never as deleted.
  await expect
    .element(view.getByTestId('recording-row').nth(2))
    .toHaveTextContent('缺失')
  // Only rows with media can be selected.
  await expect
    .element(view.getByLabelText(`选择 ${GONE}`))
    .toBeDisabled()
})

test('deleting the selection shows the confirmation scope and then per-row outcomes', async () => {
  const setup = fixture({
    outcomes: [
      { segment_id: OLD, state: 'deleted', bytes: 1024 * 1024 },
      { segment_id: PROTECTED, state: 'protected', reason: 'protected' },
    ],
  })
  const view = await render(setup.view)
  await userEvent.click(view.getByLabelText(`选择 ${OLD}`))
  await userEvent.click(view.getByLabelText(`选择 ${PROTECTED}`))
  await userEvent.click(
    view.getByRole('button', { name: /删除本地副本（2 段）/ })
  )
  // The confirmation states the scope before anything is removed, and names the
  // protected rows that will be skipped.
  await expect
    .element(view.getByText(/将释放约 1\.0 MB/))
    .toBeVisible()
  await expect.element(view.getByText(/已保护的 1 段会被跳过/)).toBeVisible()
  await userEvent.click(
    view.getByRole('button', { name: '删除本地副本', exact: true })
  )
  await expect.element(view.getByTestId('delete-outcome')).toHaveLength(2)
  const rows = view.getByTestId('delete-outcome')
  await expect.element(rows.nth(0)).toHaveTextContent('已删除')
  await expect.element(rows.nth(1)).toHaveTextContent('已保护，已跳过')
  // One request carrying the selected ids, never a per-row loop.
  const remove = setup.mutations.filter((m) =>
    m.path.endsWith('/batch-delete-local')
  )
  expect(remove).toHaveLength(1)
  expect(remove[0].body).toEqual({ segment_ids: [OLD, PROTECTED] })
})

test('protection can be toggled per row and is sent as a single PATCH', async () => {
  const setup = fixture()
  const view = await render(setup.view)
  // Each row's action carries its own recording, so rows stay distinguishable.
  await userEvent.click(
    view.getByRole('button', { name: /^保护 2026-10-04/ }).first()
  )
  const patches = setup.mutations.filter((m) =>
    m.path.startsWith('/api/v1/recordings/')
  )
  expect(patches).toHaveLength(1)
  expect(patches[0].body).toEqual({ protected: true })
})

test('the retention panel states what each mode does and saves the chosen period', async () => {
  const setup = fixture({ retentionDays: 0 })
  const view = await render(setup.view)
  // The retention card is collapsed by default; open it to reach the body.
  await userEvent.click(view.getByText('本地保留策略'))
  await expect
    .element(view.getByText(/当前不自动删除任何录像/))
    .toBeVisible()
  await userEvent.click(view.getByLabelText('保留天数'))
  await userEvent.click(view.getByRole('option', { name: '7 天', exact: true }))
  await userEvent.click(
    view.getByRole('button', { name: '保存保留策略' })
  )
  const saves = setup.mutations.filter((m) => m.path === '/api/v1/site')
  expect(saves).toHaveLength(1)
  expect(saves[0].body).toEqual({ recording_retention_days: 7 })
})

test('an enabled retention period says it deletes and that protection is skipped', async () => {
  const setup = fixture({ retentionDays: 30 })
  const view = await render(setup.view)
  // The retention card is collapsed by default; open it to reach the body.
  await userEvent.click(view.getByText('本地保留策略'))
  await expect.element(view.getByText(/超过 30 天的录像会自动删除本地副本/)).toBeVisible()
  await expect.element(view.getByText(/已保护的录像始终跳过/)).toBeVisible()
})

test('file management is administrator only', async () => {
  const setup = fixture()
  useAuthStore.getState().setUser({
    id: 'op',
    username: 'operator',
    role: 'operator',
    enabled: true,
    version: 1,
  })
  const view = await render(setup.view)
  await expect
    .element(view.getByText(/录像文件管理只对管理员开放/))
    .toBeVisible()
})
