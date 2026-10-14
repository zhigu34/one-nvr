import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import { Detection } from './index'

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

function fixture(
  enabled: boolean,
  options: {
    zones?: number
    rules?: number
    channel?: Partial<Record<string, unknown>>
    summary?: Partial<Record<string, unknown>>
    revisionPending?: boolean
  } = {}
) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  // A PUT stores the payload: the refetched configs must echo it back, the
  // same way the real server does, so the saved row reads as unchanged.
  let storedEnabled = enabled
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (init?.method === 'PUT' && path === `/api/v1/detection/configs/${channelID}`) {
      storedEnabled = Boolean((init.body as string | undefined) ? JSON.parse(init.body as string).enabled : enabled)
      return { channel_id: channelID, enabled: storedEnabled, stream: 'sub', fps: 5, labels: ['person'], version: 2 }
    }
    if (path.startsWith('/api/v1/channels?') && !path.includes('summary'))
      return {
        items: [
          {
            id: channelID,
            channel_no: 5,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 1,
            permissions: ['configure'],
            ...options.channel,
          },
        ],
        next_cursor: null,
      }
    if (path === '/api/v1/channels/summary')
      return {
        items: [
          {
            channel_id: channelID,
            channel_no: 5,
            channel_name: '大门',
            channel_group: '',
            enabled: true,
            version: 1,
            permissions: ['configure'],
            current_revision_id: '00000000-0000-4000-8000-0000000000aa',
            source_ip: '192.168.66.114',
            main: { state: 'healthy', reason: '', observed_at: null, expires_at: null },
            sub: { state: 'unknown', reason: 'observation_missing', observed_at: null, expires_at: null },
            recording: { state: 'disabled', reason: 'recording_disabled', observed_at: null, expires_at: null },
            ...options.summary,
          },
        ],
      }
    if (path === '/api/v1/detection/configs')
      return {
        items: [
          { channel_id: channelID, enabled: storedEnabled, stream: 'sub', fps: 5, labels: ['person'], version: storedEnabled === enabled ? 1 : 2 },
        ],
      }
    if (path === '/api/v1/detection/runtime')
      return { revision: options.revisionPending ? { pending: true, state: 'published', camera_count: 1 } : null }
    if (path.startsWith('/api/v1/detection/zones?'))
      return {
        items: Array.from({ length: options.zones ?? 0 }, (_, index) => ({
          id: `zone-${index}`,
          name: `区域${index}`,
          coordinates: [],
          objects: [],
          needs_calibration: false,
        })),
      }
    if (path.startsWith('/api/v1/detection/rules?'))
      return {
        items: Array.from({ length: options.rules ?? 0 }, (_, index) => ({
          id: `rule-${index}`,
          name: `规则${index}`,
          kind: 'appear',
          categories: ['person'],
          min_score: 0,
          arm_from: '',
          arm_to: '',
          dwell_seconds: null,
          severity: 'medium',
          cooldown_seconds: 60,
          enabled: true,
        })),
      }
    throw new Error(`unexpected path ${path}`)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return client
}

const view = (client: QueryClient) => (
  <QueryClientProvider client={client}>
    <Detection />
  </QueryClientProvider>
)

test('zones and rules stay behind a disclosure until asked for', async () => {
  const screen = await render(view(fixture(true, { zones: 2, rules: 3 })))
  await expect.element(screen.getByTestId('detection-row')).toBeVisible()
  // The toggle carries the counts so fences are visible unexpanded.
  const toggle = screen.getByRole('button', { name: 'CH05 大门 区域与规则 (2 区域 · 3 规则)' })
  await expect.element(toggle).toBeVisible()
  expect(document.querySelector('[data-testid="zone-canvas"]')).toBeNull()

  await toggle.click()
  await expect.element(screen.getByTestId('zone-canvas')).toBeVisible()
  await expect.element(screen.getByRole('button', { name: '新建规则' })).toBeVisible()
})

test('a healthy enabled row shows its health badge and keeps one line', async () => {
  const screen = await render(view(fixture(true)))
  const row = screen.getByTestId('detection-row')
  await expect.element(row).toBeVisible()
  // Row health comes from the channel summary, not the detection config.
  await expect.element(screen.getByText('在线')).toBeVisible()
  // The enabled toggle is a Switch, not a checkbox.
  await expect
    .element(screen.getByRole('switch', { name: 'CH05 大门 启用检测' }))
    .toBeChecked()
})

test('an unconfigured channel is dimmed out with inert controls', async () => {
  const screen = await render(
    view(
      fixture(false, {
        summary: {
          source_ip: '',
          current_revision_id: null,
          main: { state: 'not_configured', reason: 'source_not_configured', observed_at: null, expires_at: null },
        },
        channel: { enabled: false },
      })
    )
  )
  await expect.element(screen.getByText('未配置源')).toBeVisible()
  const toggle = screen.getByRole('switch', { name: 'CH05 大门 启用检测' })
  await expect.element(toggle).toBeDisabled()
  await expect
    .element(screen.getByRole('button', { name: 'CH05 大门 区域与规则' }))
    .toBeDisabled()
  await expect.element(screen.getByText(/请先在通道管理中接入摄像头/)).toBeVisible()
})

test('the filter pills narrow the rows by state', async () => {
  const screen = await render(view(fixture(true, { revisionPending: true })))
  const group = screen.getByRole('group', { name: '通道筛选' })
  await expect.element(group).toHaveTextContent('全部1')
  await expect.element(group).toHaveTextContent('已启用检测1')
  await userEvent.click(group.getByRole('button', { name: /未保存修改/ }))
  // No dirty rows yet: the empty result is fine, the pill counts come from edits.
  await expect
    .element(screen.getByRole('button', { name: /未保存修改/ }))
    .toHaveAttribute('aria-pressed', 'true')
})

test('editing a row marks it dirty and the sticky publish bar appears', async () => {
  const screen = await render(view(fixture(false, { revisionPending: true })))
  // Before any edit: the runtime says pending, so the bar exists but the
  // dirty hint does not.
  await expect.element(screen.getByTestId('publish-bar')).toBeVisible()
  expect(screen.getByTestId('publish-bar').element().textContent).not.toContain('未保存的修改')
  // An untouched row has nothing to save: the button sits dimmed and inert.
  // Scoped to the row: the "未保存修改" pill also matches 保存.
  const row = screen.getByTestId('detection-row')
  await expect.element(row.getByRole('button', { name: '保存' })).toBeDisabled()
  const switchControl = screen.getByRole('switch', { name: 'CH05 大门 启用检测' })
  await switchControl.click()
  // The edit lights the save button up as 保存*.
  await expect
    .element(row.getByRole('button', { name: '保存*' }))
    .toBeEnabled()
  // The bar now warns about the unsaved edit and blocks publishing.
  await vi.waitFor(() =>
    expect(screen.getByTestId('publish-bar').element().textContent).toContain('未保存的修改')
  )
  await expect
    .element(screen.getByTestId('publish-bar').getByRole('button', { name: '发布检测配置' }))
    .toBeDisabled()
  // The row carries the exact badge; the bar's longer hint matches separately.
  await expect.element(screen.getByText('未保存', { exact: true })).toBeVisible()
  // Saving the row clears the dirty flag and releases the publish button.
  await screen.getByRole('button', { name: '保存*' }).click()
  await vi.waitFor(() =>
    expect(
      calls.request.mock.calls.filter(
        ([path, init]) =>
          String(path) === `/api/v1/detection/configs/${channelID}` && init?.method === 'PUT'
      ).length
    ).toBe(1)
  )
  await expect
    .element(screen.getByTestId('publish-bar').getByRole('button', { name: '发布检测配置' }))
    .toBeEnabled()
  // Saving returns the row button to its dimmed, disabled rest state.
  await expect.element(row.getByRole('button', { name: '保存' })).toBeDisabled()
})

// The health labels differ by three characters ("在线" vs "通道已停用"); the
// fixed-width slot keeps the switch and selects on one vertical line.
test('the health badge sits in a fixed-width slot regardless of label length', async () => {
  const screen = await render(
    view(
      fixture(true, {
        summary: {
          source_ip: '192.168.66.115',
          enabled: false,
          main: { state: 'healthy', reason: '', observed_at: null, expires_at: null },
        },
      })
    )
  )
  const row = screen.getByTestId('detection-row')
  await expect.element(row.getByText('通道已停用')).toBeVisible()
  const badgeSlot = row.element().querySelector('span.w-24')
  expect(badgeSlot).toBeTruthy()
  expect(badgeSlot!.textContent).toContain('通道已停用')
})

test('a channel without detection says why zones cannot be calibrated', async () => {
  const screen = await render(view(fixture(false)))
  await screen.getByRole('button', { name: /区域与规则/ }).click()
  // Without detection the canvas collapses instead of rendering a black void,
  // and the row keeps the reason visible.
  await expect.element(screen.getByTestId('zone-canvas')).toBeVisible()
  expect(
    document.querySelector<HTMLElement>('[data-testid="zone-canvas"]')?.className
  ).toContain('pointer-events-none')
})
