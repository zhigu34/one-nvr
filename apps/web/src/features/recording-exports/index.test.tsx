import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { useAuthStore } from '@/stores/auth-store'
import type { Schema } from '@/lib/types'
import { ExportDialog } from './export-dialog'
import { ExportsDrawer } from './exports-drawer'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))

const base = Date.parse('2026-10-11T02:00:00Z')
const zone = 'Asia/Shanghai'

const channel: Schema<'Channel'> = {
  id: 'channel-1',
  channel_no: 1,
  channel_name: '大门',
  channel_group: '',
  enabled: true,
  version: 1,
  permissions: ['export', 'playback'],
  configured: true,
}

const analysis: Schema<'RecordingExportAnalysis'> = {
  channel_id: 'channel-1',
  start: new Date(base).toISOString(),
  end: new Date(base + 120_000).toISOString(),
  coverage_ms: 120_000,
  groups: [
    { start: new Date(base).toISOString(), end: new Date(base + 120_000).toISOString(), duration_ms: 120_000, segments: 2, bytes: 4096 },
  ],
  gaps: [],
  unavailable: [],
  duplicate_count: 0,
  ready_bytes: 4096,
  media_consistent: true,
}

const succeededExport: Schema<'RecordingExport'> = {
  id: 'export-1',
  channel_id: 'channel-1',
  channel_no: 1,
  pool_id: 'pool-1',
  requested_start: new Date(base).toISOString(),
  requested_end: new Date(base + 120_000).toISOString(),
  gap_mode: 'merge',
  state: 'succeeded',
  progress: 100,
  message: '',
  error_code: null,
  actual_start: new Date(base).toISOString(),
  actual_end: new Date(base + 120_000).toISOString(),
  requested_by_name: 'admin',
  created_at: new Date(base).toISOString(),
  artifacts: [
    {
      id: 'artifact-1',
      file_name: 'CH01_20261011_100000_001.mp4',
      size_bytes: 4096,
      sha256: 'a'.repeat(64),
      start: new Date(base).toISOString(),
      end: new Date(base + 120_000).toISOString(),
      segment_count: 2,
      state: 'ready',
      protected: false,
      expires_at: new Date(base + 7 * 86_400_000).toISOString(),
    },
  ],
}

afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})

async function mount(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

test('the dialog previews the range and submits with the chosen gap handling', async () => {
  useAuthStore.getState().setUser({ id: 'admin', username: 'admin', role: 'admin', enabled: true, version: 1 })
  const mutations: { path: string; body: unknown }[] = []
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method && init.method !== 'GET') {
      mutations.push({ path, body: JSON.parse(String(init.body)) })
      return { export_id: 'export-1', job_id: 'job-1' }
    }
    if (path.startsWith('/api/v1/storage-pools'))
      return { items: [], next_cursor: null }
    if (path.includes('/analyze')) return analysis
    if (path === '/api/v1/recording-exports/export-1') return succeededExport
    throw new Error('unexpected query ' + path)
  })
  const view = await mount(
    <ExportDialog channel={channel} zone={zone} start={base} end={base + 120_000} open onOpenChange={() => {}} />
  )
  await expect.element(view.getByText('可导出录像')).toBeVisible()
  await expect.element(view.getByText('2 分钟 · 0.0 MiB')).toBeVisible()
  await view.getByRole('combobox', { name: '缺口处理' }).click()
  await view.getByRole('option', { name: '分段：每个连续区间一个文件' }).click()
  await view.getByRole('button', { name: '开始导出' }).click()
  await vi.waitFor(() => expect(mutations).toHaveLength(1))
  expect(mutations[0]).toMatchObject({
    path: '/api/v1/recording-exports',
    body: { channel_id: 'channel-1', start: base, end: base + 120_000, gap_mode: 'split' },
  })
  // Tracking reaches the terminal state and offers both downloads.
  await expect.element(view.getByText('导出完成，产物默认保留 7 天。')).toBeVisible()
  await expect
    .element(view.getByRole('link', { name: '下载 ZIP（含证据清单）' }))
    .toHaveAttribute('href', '/api/v1/recording-exports/export-1/download')
  await expect
    .element(view.getByRole('link', { name: 'CH01_20261011_100000_001.mp4' }))
    .toHaveAttribute('href', '/api/v1/recording-exports/export-1/artifacts/artifact-1/content')
})

test('an inconsistent range refuses submission and a failed task explains itself', async () => {
  useAuthStore.getState().setUser({ id: 'admin', username: 'admin', role: 'admin', enabled: true, version: 1 })
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method && init.method !== 'GET') return { export_id: 'export-2', job_id: 'job-2' }
    if (path.includes('/analyze'))
      return { ...analysis, media_consistent: false }
    if (path === '/api/v1/recording-exports/export-2')
      return {
        ...succeededExport,
        id: 'export-2',
        state: 'failed',
        progress: 100,
        error_code: 'media_incompatible',
      }
    if (path.startsWith('/api/v1/storage-pools')) return { items: [], next_cursor: null }
    throw new Error('unexpected query ' + path)
  })
  const view = await mount(
    <ExportDialog channel={channel} zone={zone} start={base} end={base + 120_000} open onOpenChange={() => {}} />
  )
  await expect.element(view.getByText('所选片段编码参数不一致，原码拼接导出将被拒绝。请缩小范围到单一参数区间。')).toBeVisible()
  await expect.element(view.getByRole('button', { name: '开始导出' })).toBeDisabled()
})

test('the drawer lists tasks with downloads and failure reasons', async () => {
  useAuthStore.getState().setUser({ id: 'admin', username: 'admin', role: 'admin', enabled: true, version: 1 })
  calls.request.mockImplementation(async (path: string) => {
    if (path.startsWith('/api/v1/recording-exports?channel_id='))
      return {
        items: [
          succeededExport,
          {
            ...succeededExport,
            id: 'export-3',
            state: 'failed',
            progress: 100,
            error_code: 'low_space',
            artifacts: [],
          },
        ],
      }
    throw new Error('unexpected query ' + path)
  })
  const view = await mount(<ExportsDrawer channel={channel} zone={zone} open onOpenChange={() => {}} />)
  await expect.element(view.getByTestId('export-row')).toHaveLength(2)
  await expect
    .element(view.getByRole('link', { name: '下载 ZIP（含证据清单）' }).first())
    .toHaveAttribute('href', '/api/v1/recording-exports/export-1/download')
  await expect.element(view.getByText('失败原因：输出池剩余空间不足')).toBeVisible()
  // The admin sees the artifact protection toggle.
  await expect.element(view.getByRole('button', { name: '保护 CH01_20261011_100000_001.mp4' })).toBeVisible()
})
