import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { CredentialReveal } from './source-credentials'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => vi.clearAllMocks())
const props = {
  channelId: '00000000-0000-4000-8000-000000000001',
  revisionId: '00000000-0000-4000-8000-000000000010',
  allowed: true,
}
test('credentials live only in this component and hide/permission/session changes clear them', async () => {
  calls.request.mockResolvedValue({
    username: 'isolated-user',
    password: 'isolated-reveal-secret',
  })
  const view = await render(<CredentialReveal {...props} />)
  await userEvent.click(view.getByRole('button', { name: '查看账号密码' }))
  await expect
    .element(view.getByLabelText('已保存密码'))
    .toHaveValue('isolated-reveal-secret')
  await userEvent.click(view.getByRole('button', { name: '隐藏账号密码' }))
  await expect
    .element(view.getByLabelText('已保存密码'))
    .not.toBeInTheDocument()
  await userEvent.click(view.getByRole('button', { name: '查看账号密码' }))
  await view.rerender(<CredentialReveal {...props} allowed={false} />)
  await expect
    .element(view.getByLabelText('已保存密码'))
    .not.toBeInTheDocument()
  await view.rerender(<CredentialReveal {...props} />)
  await userEvent.click(view.getByRole('button', { name: '查看账号密码' }))
  window.dispatchEvent(new Event('one-nvr:unauthenticated'))
  await expect
    .element(view.getByLabelText('已保存密码'))
    .not.toBeInTheDocument()
  expect(localStorage.getItem('isolated-reveal-secret')).toBeNull()
})
test('a late reveal after hiding cannot redisplay plaintext', async () => {
  let finish: (value: { username: string; password: string }) => void = () => {}
  calls.request.mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve
      })
  )
  const view = await render(<CredentialReveal {...props} />)
  await userEvent.click(view.getByRole('button', { name: '查看账号密码' }))
  await userEvent.click(view.getByRole('button', { name: '隐藏账号密码' }))
  finish({ username: 'late-user', password: 'late-private-value' })
  await expect
    .element(view.getByLabelText('已保存密码'))
    .not.toBeInTheDocument()
})
