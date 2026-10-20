import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import { ApiError } from '@/lib/api-client'
import { ArchiveTargets } from './index'
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

async function setup(
  options: {
    targets?: unknown[]
    caps?: unknown
    failTest?: boolean
    failEvidence?: { stage: string; detail: string }
    failCode?: string
    failMessage?: string
    statuses?: unknown[]
    channelStatus?: unknown[]
    failStatus?: boolean
    retried?: number
    channels?: unknown[]
    policies?: unknown[]
    collapsed?: boolean
    openList?: { enabled: boolean; state: string; ui_port?: number } | null
  } = {}
) {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  calls.request.mockImplementation(async (path: string) => {
    if (path === '/api/v1/archive-targets/test') {
      if (options.failTest)
        throw new ApiError(
          422,
          options.failCode ?? 'archive_target_unauthorized',
          options.failMessage ?? 'WebDAV 服务拒绝了这组账号密码（HTTP 401）。',
          '',
          options.failEvidence
        )
      return options.caps ?? { write: true, read: true, range: true, delete: true }
    }
    if (path.endsWith('/retry')) return { retried: options.retried ?? 0 }
    if (path === '/api/v1/archive-targets/status') {
      if (options.failStatus) throw new Error('unavailable')
      return { items: options.statuses ?? [] }
    }
    if (path === '/api/v1/archive-channels')
      return { items: options.channelStatus ?? [] }
    if (path === '/api/v1/operations/bundled-openlist') {
      // Default: the module is off, which is the state of a stock deployment.
      return options.openList ?? { enabled: false, state: 'disabled', ui_port: 0 }
    }
    if (path.startsWith('/api/v1/archive-targets'))
      return { items: options.targets ?? [], next_cursor: null }
    if (path.startsWith('/api/v1/archive-policies'))
      return { items: options.policies ?? [] }
    if (path.startsWith('/api/v1/channels'))
      return { items: options.channels ?? [], next_cursor: null }
    if (path.startsWith('/api/v1/site'))
      return { timezone: 'Asia/Shanghai', version: 1 }
    return {}
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const page = await render(
    <QueryClientProvider client={client}>
      <ArchiveTargets />
    </QueryClientProvider>
  )
  // The form starts collapsed. Most tests below are about its fields, so they
  // open it the way an operator would; the collapse tests pass collapsed: true.
  if (!options.collapsed) {
    await userEvent.click(page.getByRole('button', { name: /登记目标|编辑目标/ }))
  }
  return page
}

test('the form labels itself and explains the retention unit', async () => {
  const page = await setup()
  await expect.element(page.getByText('登记 WebDAV 目标')).toBeVisible()
  await expect.element(page.getByText('密码加密存储')).toBeVisible()
  // The unit lives inside the field and the permanent-keep rule below it, so a
  // value of 0 is not a mystery.
  await expect.element(page.getByText('默认 365 天，填 0 为永久保留')).toBeVisible()
  await expect.element(page.getByText('天', { exact: true })).toBeVisible()
  // Placeholders that show the shape of a real answer.
  await expect
    .element(page.getByLabelText('归档目标名称'))
    .toHaveAttribute('placeholder', '例如：NAS 归档 / OpenList 云盘备份')
})

test('the form fields line up across the grid', async () => {
  // The fields carry different amounts of helper text, so bottom-alignment (or a
  // taller label row on one field) staggers them: the second row's inputs landed
  // ~30px below the first, and the password box drifted below its neighbours.
  // Asserting the grid style alone would not catch it — measure the boxes.
  const page = await setup()
  const inputTop = (label: string) =>
    page.getByLabelText(label).element().getBoundingClientRect().top
  // Row 1: 名称 / WebDAV URL / 远端根目录.
  const row1 = [
    inputTop('归档目标名称'),
    inputTop('WebDAV URL'),
    inputTop('远端根目录'),
  ]
  expect(Math.max(...row1) - Math.min(...row1)).toBeLessThan(2)
  // Row 2: 账号 / 密码 / 云端保留. The password field is a flex wrapper with a
  // reveal button, so it is the one most likely to drift.
  const row2 = [
    inputTop('WebDAV 账号'),
    inputTop('WebDAV 密码'),
    inputTop('云端保留天数'),
  ]
  expect(Math.max(...row2) - Math.min(...row2)).toBeLessThan(2)
})

test('the reveal button is flush with the password field it belongs to', async () => {
  // The two are joined into one control by dropping the facing borders, so any
  // height difference shows up as a step in the outline. size='sm' is h-8 while
  // Input is h-9, which left the button 4px short and broke the join.
  const page = await setup()
  const input = page.getByLabelText('WebDAV 密码').element().getBoundingClientRect()
  const button = page
    .getByRole('button', { name: '显示密码' })
    .element()
    .getBoundingClientRect()
  expect(Math.abs(button.height - input.height)).toBeLessThan(0.5)
  expect(Math.abs(button.top - input.top)).toBeLessThan(0.5)
  expect(Math.abs(button.bottom - input.bottom)).toBeLessThan(0.5)
  // Flush, not overlapping or gapped: the step would also be visible as a seam.
  expect(Math.abs(button.left - input.right)).toBeLessThan(1)
})

test('the password can be revealed to check a long token', async () => {
  const page = await setup()
  const field = page.getByLabelText('WebDAV 密码').element() as HTMLInputElement
  expect(field.type).toBe('password')
  await userEvent.click(page.getByRole('button', { name: '显示密码' }))
  expect((page.getByLabelText('WebDAV 密码').element() as HTMLInputElement).type).toBe(
    'text'
  )
  await userEvent.click(page.getByRole('button', { name: '隐藏密码' }))
  expect((page.getByLabelText('WebDAV 密码').element() as HTMLInputElement).type).toBe(
    'password'
  )
})

test('the OpenList shortcut fills the bundled container address', async () => {
  const page = await setup({ openList: { enabled: true, state: 'healthy', ui_port: 5244 } })
  await userEvent.click(
    page.getByRole('button', { name: '填入 OpenList 默认地址' })
  )
  await expect
    .element(page.getByLabelText('WebDAV URL'))
    .toHaveValue('http://openlist:5244/dav')
})

// The shortcut fills a private service name that resolves to nothing unless the
// bundled container ships. Offering it in a deployment without that container
// hands the operator an address whose only possible outcome is a failed probe.
test('the shortcut is absent when the bundled OpenList is off', async () => {
  const page = await setup({ openList: { enabled: false, state: 'disabled', ui_port: 0 } })
  await expect.element(page.getByText(/未启用自带的 OpenList/)).toBeVisible()
  expect(page.getByRole('button', { name: '填入 OpenList 默认地址' }).query()).toBeNull()
  // No console link either: there is no console.
  expect(page.getByText('打开 OpenList 配置页').query()).toBeNull()
  // The rest of the form still works: a self-hosted WebDAV target is the normal
  // case for a deployment without the bundled container.
  await expect.element(page.getByLabelText('WebDAV URL')).toBeVisible()
})

// The console is where the administrator account is first created, so the way in
// must be present even when the container has not passed a probe yet — before
// that account exists it cannot. It only requires that a port was published.
test('the console link is offered whenever a port is published, healthy or not', async () => {
  const page = await setup({ openList: { enabled: true, state: 'unavailable', ui_port: 5244 } })
  const link = page.getByRole('link', { name: '打开 OpenList 配置页' })
  await expect.element(link).toBeVisible()
  // The host is the one the operator reached one-nvr on, not a server guess.
  await expect.element(link).toHaveAttribute('href', 'http://localhost:5244/')
  // The fill button stays disabled: an unreachable container's private address
  // would only produce a probe failure later.
  await expect
    .element(page.getByRole('button', { name: '填入 OpenList 默认地址' }))
    .toBeDisabled()
})

test('the console link is replaced by an explanation when no port is published', async () => {
  const page = await setup({ openList: { enabled: true, state: 'healthy', ui_port: 0 } })
  expect(page.getByRole('link', { name: '打开 OpenList 配置页' }).query()).toBeNull()
  await expect.element(page.getByText(/配置页未发布/)).toBeVisible()
  // The fill button is still usable: the WebDAV address works without the console.
  await expect
    .element(page.getByRole('button', { name: '填入 OpenList 默认地址' }))
    .toBeEnabled()
})

test('the shortcut is disabled, with a reason, when the container is not reachable', async () => {
  const page = await setup({ openList: { enabled: true, state: 'unavailable' } })
  const button = page.getByRole('button', { name: '填入 OpenList 默认地址' })
  await expect.element(button).toBeVisible()
  await expect.element(button).toBeDisabled()
  await expect.element(page.getByText(/自带 OpenList 不可达/)).toBeVisible()
  // Clicking does nothing: no address is invented from a dead service.
  await expect.element(page.getByLabelText('WebDAV URL')).toHaveValue('')
})

// "We have not managed to prove it works" is not "it works". An unobserved
// component must not enable a shortcut whose whole promise is a reachable host.
test('an unobserved container counts as unavailable, not healthy', async () => {
  const page = await setup({ openList: { enabled: true, state: 'unknown' } })
  await expect
    .element(page.getByRole('button', { name: '填入 OpenList 默认地址' }))
    .toBeDisabled()
  await expect.element(page.getByText(/状态未知/)).toBeVisible()
})

// ARC-01 promises the probe verifies write, read, range and delete. A single
// green tick would hide a target that cannot DELETE, which breaks cloud expiry.
test('a full probe reports each capability inline', async () => {
  const page = await setup()
  await userEvent.click(page.getByRole('button', { name: '测试连接' }))
  await expect.element(page.getByText(/✓ 连接正常/)).toBeVisible()
  for (const label of ['写入✓', '读取✓', 'Range✓', '删除✓']) {
    await expect.element(page.getByText(label)).toBeVisible()
  }
})

test('an incomplete probe names the capability that failed', async () => {
  const page = await setup({
    caps: { write: true, read: true, range: true, delete: false },
  })
  await userEvent.click(page.getByRole('button', { name: '测试连接' }))
  await expect.element(page.getByText(/⚠ 能力不完整/)).toBeVisible()
  await expect.element(page.getByText('删除✗')).toBeVisible()
})

test('a failed probe says so instead of leaving the last result up', async () => {
  const page = await setup({ failTest: true })
  await userEvent.click(page.getByRole('button', { name: '测试连接' }))
  await expect.element(page.getByText(/测试失败/)).toBeVisible()
  // The server's own wording reaches the page; the badge no longer invents
  // "连接失败" for every kind of failure.
  await expect.element(page.getByText(/拒绝了这组账号密码/)).toBeVisible()
})

// 403 is not a credential problem. An operator whose password demonstrably works
// was told to check it, because every 4xx used to collapse into "unauthorized".
test('a read-only target is not reported as a credential problem', async () => {
  const page = await setup({
    failTest: true,
    failCode: 'archive_target_read_only',
    failMessage: '账号密码已通过（HTTP 403 而非 401），但服务端不允许往这个路径写入。',
    failEvidence: { stage: 'create-collection', detail: 'HTTP 403' },
  })
  await userEvent.click(page.getByRole('button', { name: '测试连接' }))
  await expect.element(page.getByText(/账号密码已通过/)).toBeVisible()
  await expect.element(page.getByText(/测试失败 · 创建目录 · HTTP 403/)).toBeVisible()
  expect(page.getByText(/拒绝了这组账号密码/).query()).toBeNull()
})

// "connection failed" hides whether the address, the credentials or the root
// path was wrong. The stage the server reported is what makes it actionable.
test('a failed probe names the step that failed and what the server said', async () => {
  const page = await setup({
    failTest: true,
    failEvidence: { stage: 'write', detail: 'HTTP 409' },
  })
  await userEvent.click(page.getByRole('button', { name: '测试连接' }))
  await expect.element(page.getByText(/测试失败 · 写入 · HTTP 409/)).toBeVisible()
})

test('the empty state is compact rather than a tall dashed block', async () => {
  const page = await setup()
  // The empty state renders only once the list request has settled.
  await expect.element(page.getByText(/还没有归档目标/)).toBeVisible()
  const note = page.getByText(/还没有归档目标/).element()
  // Neither the old 64px block padding nor a full-height placeholder: the empty
  // state is the normal state of a fresh install.
  expect(note.className).toContain('py-3.5')
})

const TARGET = { id: 't1', name: 'NAS 归档', enabled: true, username: 'u', cloud_retention_days: 365 }

test('a target with nothing uploaded does not read as healthy', async () => {
  // The list alone cannot tell "working" from "never tried"; the run status has
  // to say the difference rather than print a green tick from zero samples.
  const page = await setup({
    targets: [TARGET],
    statuses: [{ target_id: 't1', verified: 0, uploading: 0, failed: 0 }],
  })
  await expect.element(page.getByTestId('archive-run-status')).toBeVisible()
  await expect.element(page.getByText('尚未上传')).toBeVisible()
})

test('a failing target shows the reason and offers a retry', async () => {
  const page = await setup({
    targets: [TARGET],
    statuses: [
      { target_id: 't1', verified: 4, uploading: 0, failed: 7, last_error: 'target_unauthorized' },
    ],
    retried: 7,
  })
  await expect.element(page.getByText('有失败')).toBeVisible()
  await expect.element(page.getByText(/目标拒绝当前凭据/)).toBeVisible()
  await userEvent.click(page.getByRole('button', { name: '重试失败项' }))
  // The count comes from the response, not from a fixed string.
  await expect.element(page.getByText('已重新排队 7 项上传')).toBeVisible()
})

test('a failed status read is reported instead of rendered as all-clear', async () => {
  const page = await setup({ targets: [TARGET], failStatus: true })
  await expect.element(page.getByText(/归档运行状态读不到/)).toBeVisible()
  expect(page.getByText('已同步').query()).toBeNull()
})

// Coverage moved into the per-channel table, next to the binding it belongs to.
test('a channel row reports its binding, coverage and failures', async () => {
  const page = await setup({
    targets: [TARGET],
    statuses: [{ target_id: 't1', verified: 3, uploading: 0, failed: 0 }],
    channelStatus: [
      {
        channel_id: 'c1',
        channel_no: 1,
        name: '前门',
        target_id: 't1',
        target_name: 'NAS 归档',
        target_enabled: true,
        mode: 'immediate',
        segments: 8,
        copied: 6,
        failed: 2,
        local_retention_hours: 48,
      },
    ],
    channels: [{ id: 'c1', channel_no: 1, channel_name: '前门' }],
    policies: [{ channel_id: 'c1', target_id: 't1', mode: 'immediate', schedule: '' }],
  })
  await expect.element(page.getByText('CH01 前门')).toBeVisible()
  await expect.element(page.getByText('6/8')).toBeVisible()
  // A failure count is the one number that needs acting on.
  await expect.element(page.getByText('2', { exact: true })).toBeVisible()
})

test('a channel with nothing archived says so instead of showing zeroes', async () => {
  // 0/0 is not "0% archived": the channel has nothing to cover, and a row of
  // zeroes would read as a broken archive.
  const page = await setup({
    targets: [TARGET],
    statuses: [{ target_id: 't1', verified: 0, uploading: 0, failed: 0 }],
    channelStatus: [
      {
        channel_id: 'c2',
        channel_no: 2,
        name: '后门',
        target_id: '',
        target_name: '',
        target_enabled: false,
        mode: 'none',
        segments: 0,
        copied: 0,
        failed: 0,
        local_retention_hours: 0,
      },
    ],
    channels: [{ id: 'c2', channel_no: 2, channel_name: '后门' }],
  })
  await expect.element(page.getByText('未归档')).toBeVisible()
  expect(page.getByText('0/0').query()).toBeNull()
})

// An unbound channel can hold hundreds of local segments. Showing "0/364" reads
// as an archive that is failing, when nothing was ever asked for.
test('an unbound channel shows no coverage even when it holds footage', async () => {
  const page = await setup({
    targets: [TARGET],
    channelStatus: [
      {
        channel_id: 'c3',
        channel_no: 3,
        name: '后门-左',
        target_id: '',
        target_name: '',
        target_enabled: false,
        mode: 'none',
        segments: 364,
        copied: 0,
        failed: 0,
        local_retention_hours: 48,
      },
    ],
    channels: [{ id: 'c3', channel_no: 3, channel_name: '后门-左' }],
  })
  await expect.element(page.getByText('未归档')).toBeVisible()
  expect(page.getByText('0/364').query()).toBeNull()
})

// 16+ channels each carrying a target select, a mode select and a time box was
// sixteen places to change one setting. The bar configures; the table reports.
test('the policy table is read only and one line per channel', async () => {
  const page = await setup({
    targets: [TARGET],
    channels: [
      { id: 'c1', channel_no: 1, channel_name: '前门-左上' },
      { id: 'c2', channel_no: 2, channel_name: '前门-右上' },
    ],
  })
  await expect.element(page.getByTestId('archive-target-row')).toBeVisible()
  // Column headers, once.
  for (const head of ['通道', '归档目标', '上传模式', '已归档', '失败']) {
    await expect.element(page.getByText(head, { exact: true }).first()).toBeVisible()
  }
  const rows = page.getByTestId('archive-policy-row').elements()
  expect(rows.length).toBe(2)
  // No per-row controls: the only select controls are the two in the bar.
  const comboboxes = page.getByRole('combobox').elements()
  expect(comboboxes.length).toBe(2)
  // Select all, then the bar states the intent once.
  await userEvent.click(page.getByLabelText('全选通道'))
  await expect.element(page.getByRole('button', { name: /应用到 2 路/ })).toBeVisible()
})

// The reading order is what is configured first, then what is running, then the
// per-channel table. The table is the longest block (one row per channel, 16+
// rows); anything after it is buried below a screenful of rows.
test('the target list comes before the per-channel policy table', async () => {
  const page = await setup({
    targets: [{ id: 't1', name: 'Openlist', enabled: true, username: 'admin' }],
    statuses: [{ target_id: 't1', verified: 1, uploading: 0, failed: 0 }],
    channels: [{ id: 'c1', channel_no: 1, channel_name: '前门-左' }],
  })
  await expect.element(page.getByTestId('archive-target-row')).toBeVisible()
  const row = page.getByTestId('archive-target-row').element()
  const plan = page.getByText('通道归档策略').element()
  expect(row.compareDocumentPosition(plan) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
})

// "参数无效" says nothing about which field is missing; the bar names it before
// sending, and sends nothing.
test('applying a scheduled policy without a time names the missing field', async () => {
  const page = await setup({
    targets: [{ id: 't1', name: 'Openlist', enabled: true, username: 'admin' }],
    channels: [{ id: 'c1', channel_no: 1, channel_name: '前门-左' }],
  })
  await expect.element(page.getByTestId('archive-target-row')).toBeVisible()
  await expect.element(page.getByText('CH01 前门-左')).toBeVisible()
  await userEvent.click(page.getByLabelText('CH01 前门-左 选择'))
  await userEvent.click(page.getByLabelText('上传模式'))
  await userEvent.click(await page.getByRole('option', { name: '定时上传' }).first())
  await userEvent.click(page.getByRole('button', { name: /应用到 1 路/ }))
  await expect.element(page.getByText(/定时上传需要填写时刻/)).toBeVisible()
  const puts = calls.request.mock.calls.filter(
    ([path, init]) => path === '/api/v1/archive-policies' && (init as RequestInit | undefined)?.method === 'PUT'
  )
  expect(puts).toHaveLength(0)
})

test('the registration form is collapsed until asked for', async () => {
  const page = await setup({ collapsed: true })
  const toggle = page.getByRole('button', { name: '登记目标' })
  await expect.element(toggle).toHaveAttribute('aria-expanded', 'false')
  expect(page.getByLabelText('WebDAV URL').query()).toBeNull()

  await userEvent.click(toggle)
  await expect
    .element(page.getByRole('button', { name: '收起表单' }))
    .toHaveAttribute('aria-expanded', 'true')
  await expect.element(page.getByLabelText('WebDAV URL')).toBeVisible()
})

// Editing happens inside the form, so opening it is part of that action.
test('editing a target opens the form and fills it', async () => {
  const page = await setup({
    collapsed: true,
    targets: [{ id: 't1', name: 'Openlist', enabled: true, username: 'admin', base_url: 'http://openlist:5244/dav', remote_root: '/nvr' }],
  })
  await expect.element(page.getByTestId('archive-target-row')).toBeVisible()
  await userEvent.click(page.getByRole('button', { name: '编辑' }))
  await expect.element(page.getByLabelText('归档目标名称')).toHaveValue('Openlist')
  await expect.element(page.getByLabelText('WebDAV URL')).toHaveValue('http://openlist:5244/dav')
})

// Collapsing the form would hide the fields whose error was just reported.
test('a failed probe keeps the form open', async () => {
  const page = await setup({
    failTest: true,
    failCode: 'archive_target_read_only',
    failMessage: '账号密码已通过（HTTP 403 而非 401），但服务端不允许往这个路径写入。',
  })
  await userEvent.click(page.getByRole('button', { name: '测试连接' }))
  await expect.element(page.getByText(/账号密码已通过/)).toBeVisible()
  await expect.element(page.getByLabelText('WebDAV URL')).toBeVisible()
})

