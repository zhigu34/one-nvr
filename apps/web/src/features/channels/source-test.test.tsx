import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import type { Schema } from '@/lib/types'
import { SourceTestControls } from './source-test'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => vi.clearAllMocks())
const base = {
  channelId: '00000000-0000-4000-8000-000000000001',
  version: 8,
  revisionId: '00000000-0000-4000-8000-000000000002',
  first: false,
  hasPool: true,
  onAccepted: vi.fn(),
}
const proof: Schema<'SourceTestResult'> = {
  id: '00000000-0000-4000-8000-000000000003',
  revision_id: base.revisionId,
  state: 'succeeded',
  main: { state: 'healthy', first_frame: true },
  sub: {
    state: 'unavailable',
    first_frame: false,
    reason: 'source_unavailable',
  },
  observed_at: new Date().toISOString(),
  expires_at: new Date(Date.now() + 300000).toISOString(),
}
test('apply requires the selected revision proof and keeps test separate', async () => {
  calls.request.mockResolvedValue({
    job_id: 'test-job',
    state: 'queued',
    test_id: proof.id,
  })
  const view = await render(<SourceTestControls {...base} proof={null} />)
  await expect
    .element(view.getByRole('button', { name: '应用配置', exact: true }))
    .toBeDisabled()
  await userEvent.click(
    view.getByRole('button', { name: '测试取流', exact: true })
  )
  expect(calls.request.mock.calls[0][0]).toContain(
    `/source-revisions/${base.revisionId}/test`
  )
  await expect.element(view.getByText('测试已排队')).toBeVisible()
  await view.rerender(<SourceTestControls {...base} proof={proof} />)
  await expect
    .element(view.getByText('子流不可用，可降级应用主流'))
    .toBeVisible()
  await userEvent.click(
    view.getByRole('button', { name: '应用配置', exact: true })
  )
  const [path, init] = calls.request.mock.calls[1]
  expect(path).toContain('/source/apply')
  expect(JSON.parse(init.body)).toEqual({
    revision_id: base.revisionId,
    test_id: proof.id,
  })
  expect(new Headers(init.headers).get('If-Match')).toBe('"8"')
})
test('first continuous configuration needs a pool, recording off still permits application', async () => {
  const view = await render(
    <SourceTestControls {...base} first hasPool={false} proof={proof} />
  )
  await expect
    .element(view.getByText('请先绑定存储池，或关闭普通录像'))
    .toBeVisible()
  await expect
    .element(view.getByRole('button', { name: '应用配置', exact: true }))
    .toBeDisabled()
  await userEvent.selectOptions(view.getByLabelText('首次普通录像'), 'none')
  await expect
    .element(view.getByRole('button', { name: '应用配置', exact: true }))
    .toBeEnabled()
  await view.rerender(
    <SourceTestControls {...base} proof={{ ...proof, revision_id: 'other' }} />
  )
  await expect
    .element(view.getByRole('button', { name: '应用配置', exact: true }))
    .toBeDisabled()
})
