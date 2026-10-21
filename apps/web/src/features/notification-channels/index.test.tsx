import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import type { Schema } from '@/lib/types'
import { NotificationChannels } from './index'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})

function channelFixture(overrides: Partial<Schema<'NotificationChannel'>> = {}): Schema<'NotificationChannel'> {
  return {
    id: '00000000-0000-4000-8000-0000000000aa',
    kind: 'webhook',
    name: '值班群',
    url: 'https://chat.example.com/hook/abc',
    enabled: true,
    has_secret: true,
    last_check: undefined,
    version: 3,
    created_at: '2026-10-16T00:00:00Z',
    ...overrides,
  }
}

function fixture(items: Schema<'NotificationChannel'>[], kinds: string[] = ['inapp', 'webhook']) {
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path === '/api/v1/notification-channels' && (!init || !init.method))
      return { items, kinds }
    if (path.endsWith('/test') && init?.method === 'POST')
      return { kind: 'webhook', state: 'unavailable', status: 500, error: 'receiver answered 500' }
    if (path === '/api/v1/notification-channels' && init?.method === 'POST') return channelFixture()
    throw new Error('unexpected path ' + path + ' ' + (init?.method ?? ''))
  })
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function signIn() {
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
}

const view = (client: QueryClient) => (
  <QueryClientProvider client={client}>
    <NotificationChannels />
  </QueryClientProvider>
)

test('a channel list names the kind, the endpoint and the stored secret without revealing it', async () => {
  signIn()
  const client = fixture([channelFixture()])
  const screen = await render(view(client))
  await expect.element(screen.getByText('值班群')).toBeVisible()
  await expect.element(screen.getByText('https://chat.example.com/hook/abc')).toBeVisible()
  // "has a secret" is stated; the secret itself is never in the DOM.
  await expect.element(screen.getByText('已保存签名密钥（不回显）')).toBeVisible()
  expect(document.body.textContent).not.toContain('signing-secret')
})

test('the kind list comes from the server so a new adapter is offered without a UI change', async () => {
  signIn()
  // A build that ships a third adapter reports it; the select must offer it.
  const client = fixture([channelFixture()], ['inapp', 'webhook', 'test-sink'])
  const screen = await render(view(client))
  await userEvent.click(screen.getByLabelText('渠道类型'))
  // The unknown kind is offered verbatim: hiding it would make the adapter
  // unreachable from the only place that configures channels.
  await expect
    .element(screen.getByRole('option', { name: 'test-sink', exact: true }))
    .toBeVisible()
  await expect
    .element(screen.getByRole('option', { name: '站内提醒', exact: true }))
    .toBeVisible()
})

test('the built-in inbox needs no endpoint or secret', async () => {
  signIn()
  const client = fixture([])
  const screen = await render(view(client))
  await userEvent.click(screen.getByLabelText('渠道类型'))
  await userEvent.click(screen.getByRole('option', { name: '站内提醒', exact: true }))
  await userEvent.fill(screen.getByLabelText('渠道名称'), '站内')
  // Choosing the inbox removes the webhook-only fields rather than leaving them
  // required, because the server has no endpoint to store for it.
  expect(screen.getByLabelText('Webhook 地址').query()).toBeNull()
  expect(screen.getByLabelText('签名密钥').query()).toBeNull()
  await screen.getByRole('button', { name: '添加' }).click()
  await vi.waitFor(() =>
    expect(calls.request.mock.calls.some(([path, init]) => path === '/api/v1/notification-channels' && init?.method === 'POST')).toBe(true)
  )
  const body = calls.request.mock.calls.find(
    ([path, init]) => path === '/api/v1/notification-channels' && init?.method === 'POST'
  )?.[1]?.body as string
  expect(JSON.parse(body).url).toBe('')
  client.clear()
})

test('editing keeps the stored secret when the field is left empty', async () => {
  signIn()
  const client = fixture([channelFixture()])
  const screen = await render(view(client))
  await screen.getByRole('button', { name: '编辑' }).click()
  // The field says what leaving it empty means, because the stored secret never
  // comes back to be re-submitted.
  await expect.element(screen.getByText('签名密钥（留空保持不变）')).toBeVisible()
  await userEvent.fill(screen.getByLabelText('渠道名称'), '值班群 2')
  await screen.getByRole('button', { name: '保存' }).click()
  await vi.waitFor(() =>
    expect(calls.request.mock.calls.some(([path, init]) => path.endsWith('/' + channelFixture().id) && init?.method === 'PATCH')).toBe(true)
  )
  const patch = calls.request.mock.calls.find(([, init]) => init?.method === 'PATCH')
  // The version travels as If-Match so an edit cannot overwrite someone else's.
  expect(patch?.[1]?.headers).toMatchObject({ 'If-Match': '"3"' })
  expect(JSON.parse(patch?.[1]?.body as string).secret).toBe('')
  client.clear()
})

test('the test button reports what the receiver answered, not just that it failed', async () => {
  signIn()
  const client = fixture([channelFixture()])
  const screen = await render(view(client))
  await screen.getByRole('button', { name: '测试' }).click()
  // A webhook has no read-write-delete probe, so the only honest evidence is
  // the status the far end returned.
  await expect.element(screen.getByText(/接收端未接受/)).toBeVisible()
  await expect.element(screen.getByText(/HTTP 500/)).toBeVisible()
  client.clear()
})

test('an unavailable last check is shown as a measurement, not as a working channel', async () => {
  signIn()
  const client = fixture([
    channelFixture({
      last_check: { state: 'unavailable', status: 401 } as never,
    }),
  ])
  const screen = await render(view(client))
  await expect.element(screen.getByText('上次测试失败')).toBeVisible()
})

test('a viewer is not shown channel configuration at all', async () => {
  useAuthStore.getState().setUser({
    id: 'viewer',
    username: 'viewer',
    role: 'viewer',
    enabled: true,
    version: 1,
  })
  const client = fixture([channelFixture()])
  const screen = await render(view(client))
  await expect.element(screen.getByText('通知渠道只对管理员开放。')).toBeVisible()
  // The endpoint that names internal URLs and holds secrets is never requested.
  expect(calls.request.mock.calls.length).toBe(0)
})

// The Email tab follows the operator's existing mental model — sender, server,
// then authentication — because that is the order every NVR's Email page uses and
// reordering it would make them hunt.
test('the email form offers the SMTP parameters in the familiar order', async () => {
  signIn()
  const screen = await render(view(fixture([], ['inapp', 'webhook', 'email'])))
  await userEvent.click(screen.getByLabelText('渠道类型'))
  await userEvent.click(await screen.getByRole('option', { name: '邮件（SMTP）' }).first())

  for (const label of ['发件人名称', '发件人地址', 'SMTP 服务器', 'SMTP 端口']) {
    await expect.element(screen.getByLabelText(label)).toBeVisible()
  }
  // The SSL and authentication switches are checkboxes with the operator's words.
  await expect.element(screen.getByText('启用 SSL')).toBeVisible()
  await expect.element(screen.getByText('服务器认证')).toBeVisible()
  // Credentials only appear once authentication is on: fields that would be
  // ignored are worse than absent fields.
  expect(screen.getByLabelText('SMTP 用户名').query()).toBeNull()
  await screen.getByText('服务器认证').click()
  await expect.element(screen.getByLabelText('SMTP 用户名')).toBeVisible()
  await expect.element(screen.getByLabelText('SMTP 密码')).toBeVisible()
  // The recipient editor is part of the same form.
  await expect.element(screen.getByRole('heading', { name: '收件人' })).toBeVisible()
})

test('saving an email channel sends the SMTP block and the mailbox list', async () => {
  signIn()
  const screen = await render(view(fixture([], ['inapp', 'webhook', 'email'])))
  await userEvent.click(screen.getByLabelText('渠道类型'))
  await userEvent.click(await screen.getByRole('option', { name: '邮件（SMTP）' }).first())

  await userEvent.fill(screen.getByLabelText('渠道名称').element() as HTMLInputElement, '值班邮箱')
  await userEvent.fill(screen.getByLabelText('发件人名称').element() as HTMLInputElement, '门岗值班')
  await userEvent.fill(screen.getByLabelText('发件人地址').element() as HTMLInputElement, 'nvr@example.com')
  await userEvent.fill(screen.getByLabelText('SMTP 服务器').element() as HTMLInputElement, 'smtp.example.com')
  await userEvent.fill(screen.getByLabelText('SMTP 端口').element() as HTMLInputElement, '587')
  await screen.getByText('添加一行').click()
  await userEvent.fill(
    screen.getByLabelText('收件人地址 1').element() as HTMLInputElement,
    'duty@example.com'
  )
  // The submit button, not the "添加一行" recipient control.
  await userEvent.click(screen.getByRole('button', { name: '添加', exact: true }))

  const post = calls.request.mock.calls.find(
    ([path, init]) => path === '/api/v1/notification-channels' && (init as RequestInit)?.method === 'POST'
  )
  const body = JSON.parse(String((post?.[1] as RequestInit | undefined)?.body ?? '{}'))
  expect(body.kind).toBe('email')
  expect(body.email).toMatchObject({
    sender_name: '门岗值班',
    sender_address: 'nvr@example.com',
    smtp_host: 'smtp.example.com',
    smtp_port: 587,
  })
  // A blank row the operator added but never filled is not a mailbox.
  expect(body.recipients).toEqual([{ name: '', address: 'duty@example.com' }])
})

// Each mailbox has its own test button: a refused address is a fact about that
// address, and a channel-level verdict would leave the operator guessing.
test('each recipient has its own test button', async () => {
  signIn()
  const mailboxes = [
    {
      id: '00000000-0000-4000-8000-0000000000c1',
      name: '值班',
      address: 'duty@example.com',
      last_check: { state: 'healthy' as const, address: 'duty@example.com', checked_at: '2026-10-22T09:00:00Z' },
    },
    {
      id: '00000000-0000-4000-8000-0000000000c2',
      name: '',
      address: 'oncall@example.com',
      last_check: {
        state: 'unavailable' as const,
        address: 'oncall@example.com',
        error: '550 no such user',
        checked_at: '2026-10-22T09:00:00Z',
      },
    },
  ]
  const channel = channelFixture({
    kind: 'email',
    name: '值班邮箱',
    url: undefined,
    email: {
      sender_name: '门岗值班',
      sender_address: 'nvr@example.com',
      smtp_host: 'smtp.example.com',
      smtp_port: 587,
      smtp_tls: false,
      smtp_auth: true,
      username: 'relay-user',
    },
    recipients: mailboxes,
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path === '/api/v1/notification-channels' && (!init || !init.method))
      return { items: [channel], kinds: ['inapp', 'webhook', 'email'] }
    if (path.includes('/recipients/') && path.endsWith('/test') && init?.method === 'POST')
      return { state: 'healthy', address: 'oncall@example.com' }
    throw new Error('unexpected path ' + path)
  })
  const screen = await render(view(client))

  // The summary line names the server and the two switches, so the operator can
  // tell at a glance which mode this channel runs in.
  await expect.element(screen.getByText(/smtp\.example\.com:587/)).toBeVisible()
  await expect.element(screen.getByText(/· 认证/)).toBeVisible()
  // Two mailboxes, each with its own outcome and its own test.
  await expect.element(screen.getByTestId('notification-recipient')).toHaveLength(2)
  await expect.element(screen.getByText('上次测试成功')).toBeVisible()
  await expect.element(screen.getByText('上次测试失败')).toBeVisible()

  const rows = screen.getByTestId('notification-recipient')
  await userEvent.click(rows.nth(1).getByRole('button', { name: '测试' }))
  const test = calls.request.mock.calls.find(([path]) =>
    String(path).endsWith('/recipients/00000000-0000-4000-8000-0000000000c2/test')
  )
  expect(test).toBeDefined()
})
