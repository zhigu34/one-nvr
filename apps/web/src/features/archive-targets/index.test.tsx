import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
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

function setup(
  options: {
    targets?: unknown[]
    caps?: unknown
    failTest?: boolean
    statuses?: unknown[]
    channelStatus?: unknown[]
    failStatus?: boolean
    retried?: number
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
      if (options.failTest) throw new Error('archive_target_unavailable')
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
    if (path.startsWith('/api/v1/archive-policies')) return { items: [] }
    if (path.startsWith('/api/v1/channels'))
      return { items: [], next_cursor: null }
    if (path.startsWith('/api/v1/site'))
      return { timezone: 'Asia/Shanghai', version: 1 }
    return {}
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <ArchiveTargets />
    </QueryClientProvider>
  )
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
  await expect.element(page.getByText('连接失败')).toBeVisible()
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

test('the channel overview reports coverage and the local retention window', async () => {
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
        failed: 0,
        local_retention_hours: 48,
      },
    ],
  })
  await expect.element(page.getByTestId('archive-channel-overview')).toBeVisible()
  await expect.element(page.getByText('CH01 前门')).toBeVisible()
  await expect.element(page.getByText('6/8 段已归档')).toBeVisible()
  await expect.element(page.getByText('48 小时')).toBeVisible()
})

test('a channel with no ready footage gets no coverage bar', async () => {
  // 0/0 is not 0%: an empty channel has nothing to cover, and a 0% bar would
  // read as a broken archive.
  const page = await setup({
    targets: [TARGET],
    statuses: [{ target_id: 't1', verified: 0, uploading: 0, failed: 0 }],
    channelStatus: [
      {
        channel_id: 'c2',
        channel_no: 2,
        name: '后门',
        target_id: 't1',
        target_name: 'NAS 归档',
        target_enabled: false,
        mode: 'manual',
        segments: 0,
        copied: 0,
        failed: 0,
        local_retention_hours: 0,
      },
    ],
  })
  await expect.element(page.getByText('本机暂无待归档片段')).toBeVisible()
  // The disabled target keeps its binding, and that is worth naming.
  await expect.element(page.getByText('已停用')).toBeVisible()
  await expect
    .element(page.getByTestId('archive-channel-row').getByText('未启用'))
    .toBeVisible()
})
