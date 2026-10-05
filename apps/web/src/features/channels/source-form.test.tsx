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
test('keep and clear submit explicit intent without a masked password', async () => {
  calls.request.mockResolvedValue(revision)
  const view = await render(wrapper())
  await userEvent.click(view.getByRole('button', { name: '保存草稿' }))
  let body = JSON.parse(calls.request.mock.calls[0][1].body)
  expect(body.credentials.password_action).toBe('keep')
  expect(body.credentials).not.toHaveProperty('password')
  await userEvent.selectOptions(view.getByLabelText('密码处理'), 'clear')
  await userEvent.click(view.getByRole('button', { name: '保存草稿' }))
  body = JSON.parse(calls.request.mock.calls[1][1].body)
  expect(body.credentials.password_action).toBe('clear')
  expect(body.credentials.password || '').toBe('')
})
test('replace rejects mask and sends only an explicitly entered real password', async () => {
  calls.request.mockResolvedValue(revision)
  const view = await render(wrapper())
  await userEvent.selectOptions(view.getByLabelText('密码处理'), 'replace')
  await userEvent.fill(view.getByLabelText('新密码', { exact: true }), '••••••')
  await userEvent.click(view.getByRole('button', { name: '保存草稿' }))
  expect(calls.request).not.toHaveBeenCalled()
  await expect
    .element(view.getByRole('alert'))
    .toHaveTextContent('不能保存掩码')
  await userEvent.fill(
    view.getByLabelText('新密码', { exact: true }),
    'isolated-source-password'
  )
  await userEvent.click(view.getByRole('button', { name: '保存草稿' }))
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
  await userEvent.click(view.getByRole('button', { name: '保存草稿' }))
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
  await userEvent.selectOptions(view.getByLabelText('密码处理'), 'replace')
  await userEvent.fill(
    view.getByLabelText('新密码', { exact: true }),
    'isolated-draft-secret'
  )
  await userEvent.click(view.getByRole('button', { name: '保存草稿' }))
  await expect
    .element(view.getByText('草稿已保存，请先测试，再应用'))
    .toBeVisible()
  await view.rerender(
    wrapper(
      { ...channel, version: 4 },
      { ...revision, id: '00000000-0000-4000-8000-000000000012' }
    )
  )
  await expect
    .element(view.getByLabelText('新密码', { exact: true }))
    .toHaveValue('')
  await expect
    .element(view.getByText('草稿已保存，请先测试，再应用'))
    .toBeVisible()
})
