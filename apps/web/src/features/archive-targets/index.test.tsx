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

function setup(options: { targets?: unknown[]; caps?: unknown; failTest?: boolean } = {}) {
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
  const page = await setup()
  await userEvent.click(
    page.getByRole('button', { name: '填入 OpenList 默认地址' })
  )
  await expect
    .element(page.getByLabelText('WebDAV URL'))
    .toHaveValue('http://openlist:5244/dav')
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
