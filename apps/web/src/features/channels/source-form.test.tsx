import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import type { Schema } from '@/lib/types'
import { SourceForm } from './source-form'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => vi.clearAllMocks())
const channel: Schema<'Channel'> = {
  id: '00000000-0000-4000-8000-000000000001',
  channel_no: 1,
  channel_name: 'Front',
  version: 3,
  permissions: ['configure'],
}
const revision: Schema<'SourceRevision'> = {
  id: '00000000-0000-4000-8000-000000000010',
  channel_id: channel.id,
  source_id: '00000000-0000-4000-8000-000000000011',
  number: 1,
  config: {
    ip: '192.168.33.20',
    rtsp_port: 554,
    main_path: '/main',
    sub_path: '/sub',
    transport: 'tcp',
  },
  username_summary: 'a***',
  password_state: 'saved',
  created_at: '2026-10-05T00:00:00Z',
}
function wrapper(
  ch = channel,
  rev: Schema<'SourceRevision'> | null = revision,
  saved = vi.fn()
) {
  return (
    <QueryClientProvider client={new QueryClient()}>
      <SourceForm
        channel={ch}
        revision={rev}
        history={rev ? [rev] : []}
        onSaved={saved}
      />
    </QueryClientProvider>
  )
}
test('normal editing keeps untouched credentials and accepts a typed replacement without extra switches', async () => {
  calls.request.mockResolvedValue(revision)
  const view = await render(wrapper())
  await userEvent.click(view.getByRole('button', { name: '保存并启用' }))
  let body = JSON.parse(calls.request.mock.calls[0][1].body)
  expect(body.credentials).toEqual({ password_action: 'keep' })
  await userEvent.fill(
    view.getByLabelText('用户名', { exact: true }),
    'new-camera-user'
  )
  await userEvent.fill(
    view.getByLabelText('密码', { exact: true }),
    'new-isolated-password'
  )
  await userEvent.click(view.getByRole('button', { name: '保存并启用' }))
  body = JSON.parse(calls.request.mock.calls[1][1].body)
  expect(body.credentials).toEqual({
    username: 'new-camera-user',
    password_action: 'replace',
    password: 'new-isolated-password',
  })
})
test('saving works when ordinary HTTP has no crypto.randomUUID', async () => {
  const descriptor = Object.getOwnPropertyDescriptor(crypto, 'randomUUID')
  Object.defineProperty(crypto, 'randomUUID', {
    configurable: true,
    value: undefined,
  })
  try {
    calls.request.mockResolvedValue(revision)
    const view = await render(wrapper())
    await userEvent.click(view.getByRole('button', { name: '保存并启用' }))
    expect(calls.request).toHaveBeenCalledOnce()
    expect(calls.request.mock.calls[0][1].headers['Idempotency-Key']).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
    )
  } finally {
    if (descriptor) Object.defineProperty(crypto, 'randomUUID', descriptor)
    else Reflect.deleteProperty(crypto, 'randomUUID')
  }
})
test('keep and clear submit explicit intent without a masked password', async () => {
  calls.request.mockResolvedValue(revision)
  const view = await render(wrapper())
  await userEvent.click(view.getByRole('button', { name: '保存并启用' }))
  let body = JSON.parse(calls.request.mock.calls[0][1].body)
  expect(body.credentials.password_action).toBe('keep')
  expect(body.credentials).not.toHaveProperty('password')
  await userEvent.click(view.getByText('高级连接设置', { exact: true }))
  await userEvent.selectOptions(
    view.getByLabelText('密码处理', { exact: true }),
    'clear'
  )
  await userEvent.click(view.getByRole('button', { name: '保存并启用' }))
  body = JSON.parse(calls.request.mock.calls[1][1].body)
  expect(body.credentials.password_action).toBe('clear')
  expect(body.credentials.password || '').toBe('')
})
test('replace rejects mask and sends only an explicitly entered real password', async () => {
  calls.request.mockResolvedValue(revision)
  const view = await render(wrapper())
  await userEvent.click(view.getByText('高级连接设置', { exact: true }))
  await userEvent.selectOptions(
    view.getByLabelText('密码处理', { exact: true }),
    'replace'
  )
  await userEvent.fill(view.getByLabelText('密码', { exact: true }), '••••••')
  await userEvent.click(view.getByRole('button', { name: '保存并启用' }))
  expect(calls.request).not.toHaveBeenCalled()
  await expect
    .element(view.getByRole('alert'))
    .toHaveTextContent('不能保存掩码')
  await userEvent.fill(
    view.getByLabelText('密码', { exact: true }),
    'isolated-source-password'
  )
  await userEvent.click(view.getByRole('button', { name: '保存并启用' }))
  expect(
    JSON.parse(calls.request.mock.calls[0][1].body).credentials.password
  ).toBe('isolated-source-password')
})
test('late saved revision cannot fill or activate a different selected channel', async () => {
  let finish: (value: Schema<'SourceRevision'>) => void = () => {}
  calls.request.mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve
      })
  )
  const saved = vi.fn(),
    view = await render(wrapper(channel, revision, saved))
  await userEvent.click(view.getByRole('button', { name: '保存并启用' }))
  const next = {
    ...channel,
    id: '00000000-0000-4000-8000-000000000002',
    channel_no: 2,
  }
  await view.rerender(wrapper(next, null, saved))
  finish(revision)
  await expect.element(view.getByLabelText('IP 地址')).toHaveValue('')
  expect(saved).not.toHaveBeenCalled()
})

test('changing a revision clears typed credentials but preserves a saved-operation notice', async () => {
  calls.request.mockResolvedValue(revision)
  const view = await render(wrapper())
  await userEvent.click(view.getByText('高级连接设置', { exact: true }))
  await userEvent.selectOptions(
    view.getByLabelText('密码处理', { exact: true }),
    'replace'
  )
  await userEvent.fill(
    view.getByLabelText('密码', { exact: true }),
    'isolated-draft-secret'
  )
  await userEvent.click(view.getByRole('button', { name: '保存并启用' }))
  await expect.element(view.getByText('配置已保存')).toBeVisible()
  await view.rerender(
    wrapper(
      { ...channel, version: 4 },
      { ...revision, id: '00000000-0000-4000-8000-000000000012' }
    )
  )
  await expect
    .element(view.getByLabelText('密码', { exact: true }))
    .toHaveValue('')
  await userEvent.fill(
    view.getByLabelText('主流路径', { exact: true }),
    '/main-edited'
  )
  await userEvent.click(view.getByRole('button', { name: '保存并启用' }))
  const nextBody = JSON.parse(calls.request.mock.calls[1][1].body)
  expect(nextBody.credentials.password_action).toBe('keep')
  expect(nextBody.credentials).not.toHaveProperty('password')
  expect(nextBody.identity_intent).toBe('modify')
  await expect.element(view.getByText('配置已保存')).toBeVisible()
})

test('Enter from a normal connection field runs the primary connect action', async () => {
  calls.request.mockResolvedValue(revision)
  const saved = vi.fn()
  const view = await render(wrapper(channel, revision, saved))
  await userEvent.click(view.getByLabelText('IP 地址', { exact: true }))
  await userEvent.keyboard('{Enter}')
  await expect.poll(() => saved.mock.calls.length).toBe(1)
  expect(saved.mock.calls[0][2].action).toBe('connect')
})

test('saving alone records the camera without demanding a stream test', async () => {
  calls.request.mockResolvedValue(revision)
  const saved = vi.fn(),
    view = await render(wrapper(channel, revision, saved))
  await userEvent.fill(
    view.getByLabelText('主流路径', { exact: true }),
    '/main-kept'
  )
  await userEvent.click(view.getByRole('button', { name: '保存', exact: true }))
  expect(calls.request).toHaveBeenCalledOnce()
  expect(calls.request.mock.calls[0][0]).toContain('/source-revisions')
  expect(saved.mock.calls[0][2].action).toBe('save')
})

test('ONVIF is offered as a disabled placeholder, never as usable capability', async () => {
  calls.request.mockResolvedValue(revision)
  const view = await render(wrapper())
  await expect
    .element(view.getByRole('button', { name: 'ONVIF 发现（后续开放）' }))
    .toBeDisabled()
  await expect
    .element(view.getByRole('button', { name: 'RTSP 手动' }))
    .not.toBeDisabled()
})

test('an existing ONVIF port survives an ordinary edit that cannot set it', async () => {
  calls.request.mockResolvedValue(revision)
  const view = await render(
    wrapper(channel, { ...revision, config: { ...revision.config, onvif_port: 8000 } })
  )
  await userEvent.click(view.getByRole('button', { name: '保存并启用' }))
  expect(JSON.parse(calls.request.mock.calls[0][1].body).config.onvif_port).toBe(
    8000
  )
})
