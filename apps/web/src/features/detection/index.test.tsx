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
            configured: true,
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
  const toggle = screen.getByRole('button', { name: 'CH05 大门 区域与规则 (2/3)' })
  await expect.element(toggle).toBeVisible()
  expect(document.querySelector('[data-testid="zone-canvas"]')).toBeNull()

  await toggle.click()
  await expect.element(screen.getByTestId('zone-canvas')).toBeVisible()
  await expect.element(screen.getByRole('button', { name: '新建规则' })).toBeVisible()
})

test('a healthy enabled row reports its state and settings, with no controls of its own', async () => {
  const screen = await render(view(fixture(true)))
  const row = screen.getByTestId('detection-row')
  await expect.element(row).toBeVisible()
  // Row health comes from the channel summary, not the detection config.
  await expect.element(screen.getByText('在线')).toBeVisible()
  await expect.element(row.getByText('已启用')).toBeVisible()
  // The current settings are shown as text: the operator reads them here and
  // changes them in the bar, so there is exactly one place a setting can be set.
  await expect.element(row).toHaveTextContent('子码流')
  await expect.element(row).toHaveTextContent('5 fps')
  await expect.element(row).toHaveTextContent('标准')
  await expect.element(row).toHaveTextContent('人')
  // No per-row editing controls survive the move to the bar.
  expect(row.getByRole('switch').query()).toBeNull()
  expect(row.getByRole('button', { name: /检测码流|检测帧率|检测灵敏度|目标类别/ }).query()).toBeNull()
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
  const toggle = screen.getByRole('button', { name: /CH05 大门 区域与规则/ })
  await expect.element(toggle).toBeDisabled()
  await expect.element(screen.getByText(/先在通道管理接入摄像头/)).toBeVisible()
})

test('the filter pills narrow the rows by state', async () => {
  const screen = await render(view(fixture(true, { revisionPending: true })))
  const group = screen.getByRole('group', { name: '通道筛选' })
  await expect.element(group).toHaveTextContent('全部1')
  await expect.element(group).toHaveTextContent('已启用检测1')
  await expect.element(group).toHaveTextContent('未配置源0')
  await userEvent.click(group.getByRole('button', { name: /未配置源/ }))
  // This channel has a source, so the unconfigured filter empties the table.
  await expect
    .element(screen.getByRole('button', { name: /未配置源/ }))
    .toHaveAttribute('aria-pressed', 'true')
  await expect.element(screen.getByText('没有匹配的通道')).toBeVisible()
})

test('the bar applies to the selected channel and writes what the row then shows', async () => {
  const screen = await render(view(fixture(false, { revisionPending: true })))
  await expect.element(screen.getByTestId('publish-bar')).toBeVisible()
  // Nothing selected: apply is not available.
  await expect
    .element(screen.getByRole('button', { name: /应用到所选通道/ }))
    .toBeDisabled()

  // Selecting the row echoes its stored settings into the bar. Clicking the row
  // anywhere outside a control selects it.
  await screen.getByTestId('detection-row').click()
  await expect.element(screen.getByText(/已选 1 \/ 1 路/)).toBeVisible()
  // With no edit yet there is still nothing to apply: applying would rewrite the
  // same values on every selected channel.
  await expect
    .element(screen.getByRole('button', { name: /应用到所选通道/ }))
    .toBeDisabled()

  await screen.getByRole('switch', { name: '批量启用检测' }).click()
  const apply = screen.getByRole('button', { name: /应用到所选通道/ })
  await expect.element(apply).toBeEnabled()
  await userEvent.click(apply)
  await vi.waitFor(() =>
    expect(
      calls.request.mock.calls.filter(
        ([path, init]) =>
          String(path) === `/api/v1/detection/configs/${channelID}` &&
          init?.method === 'PUT'
      ).length
    ).toBe(1)
  )
  const put = calls.request.mock.calls.find(
    ([path, init]) =>
      String(path) === `/api/v1/detection/configs/${channelID}` &&
      (init as RequestInit | undefined)?.method === 'PUT'
  )
  const body = JSON.parse(String((put?.[1] as RequestInit | undefined)?.body ?? '{}'))
  // Only the changed field moves; the rest keep the channel's own values.
  expect(body.enabled).toBe(true)
  expect(body.stream).toBe('sub')
  expect(body.fps).toBe(5)
  expect(body.labels).toEqual(['person'])
  await expect.element(screen.getByText(/已应用 1 路检测配置/)).toBeVisible()
})

test('applying leaves untouched fields at each channel\'s own value', async () => {
  const screen = await render(view(fixture(true)))
  // The stored config has stream 'sub'; change only the frame rate.
  await screen.getByTestId('detection-row').click()
  await userEvent.click(screen.getByLabelText('批量检测帧率'))
  await userEvent.click(await screen.getByRole('option', { name: '15 fps' }).first())
  await userEvent.click(screen.getByRole('button', { name: /应用到所选通道/ }))
  await vi.waitFor(() =>
    expect(
      calls.request.mock.calls.filter(
        ([path, init]) =>
          String(path) === `/api/v1/detection/configs/${channelID}` &&
          init?.method === 'PUT'
      ).length
    ).toBe(1)
  )
  const put = calls.request.mock.calls.find(
    ([path, init]) =>
      String(path) === `/api/v1/detection/configs/${channelID}` &&
      (init as RequestInit | undefined)?.method === 'PUT'
  )
  const body = JSON.parse(String((put?.[1] as RequestInit | undefined)?.body ?? '{}'))
  expect(body.fps).toBe(15)
  // The stream was not edited, so it must not be rewritten from the bar's echo.
  expect(body.stream).toBe('sub')
  expect(body.enabled).toBe(true)
})

// The health labels differ by three characters ("在线" vs "通道已停用"); the
// fixed-width slot keeps the switch and selects on one vertical line.
// The health badge is its own table column now, so column alignment replaces the
// old fixed-width span; what still matters is that a long label renders whole.
test('a disabled channel shows its state without truncation', async () => {
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

// Sensitivity is the control an operator reaches for when the detector is too
// eager or too shy. It must be visible per channel and must travel in the saved
// payload — a control that renders but is not sent is worse than none.
test('sensitivity is edited in the bar and carried into the write', async () => {
  const screen = await render(view(fixture(true)))
  await screen.getByTestId('detection-row').click()
  const select = screen.getByLabelText('批量检测灵敏度')
  await expect.element(select).toBeVisible()
  await userEvent.click(select)
  await userEvent.click(await screen.getByRole('option', { name: '灵敏' }).first())
  await userEvent.click(screen.getByRole('button', { name: /应用到所选通道/ }))
  await vi.waitFor(() =>
    expect(
      calls.request.mock.calls.filter(
        ([path, init]) =>
          String(path) === `/api/v1/detection/configs/${channelID}` &&
          init?.method === 'PUT'
      ).length
    ).toBe(1)
  )
  const put = calls.request.mock.calls.find(
    ([path, init]) =>
      String(path) === `/api/v1/detection/configs/${channelID}` &&
      (init as RequestInit | undefined)?.method === 'PUT'
  )
  const body = JSON.parse(String((put?.[1] as RequestInit | undefined)?.body ?? '{}'))
  expect(body.sensitivity).toBe('sensitive')
})

// The selection used to live only on row clicks, which meant the header had no
// way to say "all of these". The header checkbox is now the third path to the
// same set, and it must agree with the rows rather than keep its own state.
test('the header checkbox selects every actionable channel and mirrors the rows', async () => {
  const screen = await render(view(fixture(true)))
  const header = screen.getByRole('checkbox', { name: '全选可配置通道' })
  const rowBox = screen.getByRole('checkbox', { name: '选择 CH05 大门' })
  await expect.element(rowBox).not.toBeChecked()

  await userEvent.click(header)
  await expect.element(screen.getByText(/已选 1 \/ 1 路/)).toBeVisible()
  await expect.element(rowBox).toBeChecked()

  // Unticking one row takes the header out of "all": a header that stayed
  // checked would claim a set the operator no longer has.
  await userEvent.click(rowBox)
  await expect.element(rowBox).not.toBeChecked()
  await expect.element(screen.getByText(/已选 0 \/ 1 路/)).toBeVisible()
})

// "Apply" is only meaningful once there is both a target and a change. The
// reason used to be a line of text under the bar, far from the disabled button
// the operator was looking at; it is now the button's own title.
test('apply explains why it is disabled on the button itself', async () => {
  const screen = await render(view(fixture(true)))
  const apply = screen.getByRole('button', { name: /应用到所选通道/ })
  await expect.element(apply).toBeDisabled()
  await expect.element(apply).toHaveAttribute('title', '先在下方勾选要修改的通道')

  // Selected but unedited: applying would rewrite each channel's own values.
  await screen.getByTestId('detection-row').click()
  await expect.element(apply).toBeDisabled()
  await expect.element(apply).toHaveAttribute('title', '先修改上面任意一项设置')
  // The old standalone hint is gone: the button carries it now.
  expect(screen.getByText('先勾选要修改的通道').query()).toBeNull()
})

// The two columns answer different questions and must not look like the same
// kind of fact: "在线" is a measurement that changes on its own, "已启用" is a
// configuration value this page writes. So the health keeps a compact dot and
// the detection flag is a badge — neither is allowed to become a coloured pill
// on the healthy case that competes with the one row that is actually offline.
test('health is a dot and detection is a badge, not two look-alike pills', async () => {
  const screen = await render(view(fixture(true)))
  const row = screen.getByTestId('detection-row')
  await expect.element(row).toHaveTextContent('在线')
  await expect.element(row).toHaveTextContent('已启用')
  const el = row.element()

  // Exactly one badge in the row: the detection flag. Health stays a dot.
  expect(el.querySelectorAll('[data-slot="badge"]').length).toBe(1)
  expect(el.querySelector('[data-slot="badge"]')?.textContent).toContain('已启用')
  // And it is the subtle variant, not an alert: no destructive/warning tone.
  const badge = el.querySelector('[data-slot="badge"]')
  expect(badge?.className).toContain('emerald')
})
