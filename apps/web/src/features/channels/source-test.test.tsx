import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { ApiError } from '@/lib/api-client'
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
test('enabling requires the selected revision proof and keeps testing separate', async () => {
  calls.request.mockResolvedValue({
    job_id: 'test-job',
    state: 'queued',
    test_id: proof.id,
  })
  const view = await render(<SourceTestControls {...base} proof={null} />)
  await expect
    .element(view.getByRole('button', { name: '测试并启用', exact: true }))
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
    view.getByRole('button', { name: '测试并启用', exact: true })
  )
  const [path, init] = calls.request.mock.calls[1]
  expect(path).toContain('/source/apply')
  expect(JSON.parse(init.body)).toEqual({
    revision_id: base.revisionId,
    test_id: proof.id,
  })
  expect(new Headers(init.headers).get('If-Match')).toBe('"8"')
})
test('a first apply starts stream-only and offers no recording choice here', async () => {
  calls.request.mockResolvedValue({
    job_id: 'apply-job',
    state: 'queued',
    test_id: proof.id,
  })
  const view = await render(
    <SourceTestControls {...base} first proof={proof} />
  )
  // Recording belongs to the recording plan page, so this control must not ask
  // for a mode and must not gate enablement on a storage pool.
  expect(view.getByLabelText('首次普通录像').elements()).toHaveLength(0)
  await expect
    .element(view.getByRole('button', { name: '测试并启用', exact: true }))
    .toBeEnabled()
  await userEvent.click(
    view.getByRole('button', { name: '测试并启用', exact: true })
  )
  expect(JSON.parse(calls.request.mock.calls[0][1].body)).toEqual({
    revision_id: base.revisionId,
    test_id: proof.id,
    first_recording_mode: 'none',
  })
})
test('a proof bound to another revision never enables the selected one', async () => {
  const view = await render(<SourceTestControls {...base} proof={proof} />)
  await view.rerender(
    <SourceTestControls {...base} proof={{ ...proof, revision_id: 'other' }} />
  )
  await expect
    .element(view.getByRole('button', { name: '测试并启用', exact: true }))
    .toBeDisabled()
})
test('enabling recovers an expired write proof before applying', async () => {
  let applies = 0
  calls.request.mockImplementation(async (path: string) => {
    if (path.endsWith('/source/apply')) {
      applies++
      if (applies === 1)
        throw new ApiError(
          409,
          'zlm_write_evidence_unavailable',
          '存储池检查未通过，请先运行存储池检查'
        )
      return { job_id: 'apply-job', state: 'queued', test_id: proof.id }
    }
    if (path.endsWith('/source/status'))
      return { channel_id: base.channelId, storage_pool_id: 'pool', version: 8 }
    if (path.includes('/storage-pools/') && path.endsWith('/test'))
      return { job_id: 'check-job' }
    if (path.includes('/jobs/')) return { state: 'succeeded' }
    if (path.startsWith('/api/v1/storage-pools'))
      return {
        items: [
          {
            id: 'pool',
            checks: [
              {
                service: 'zlm',
                state: 'healthy',
                reason: 'zlm_media_verified',
                total_bytes: 0,
                free_bytes: 0,
                filesystem_id: '',
              },
            ],
          },
        ],
        next_cursor: null,
      }
    throw new Error('unexpected ' + path)
  })
  const view = await render(<SourceTestControls {...base} proof={proof} />)
  await userEvent.click(
    view.getByRole('button', { name: '测试并启用', exact: true })
  )
  await expect
    .element(view.getByText('启用已排队，等待执行结果'))
    .toBeVisible()
  expect(applies).toBe(2)
  expect(base.onAccepted).toHaveBeenCalledWith(
    expect.objectContaining({ job_id: 'apply-job' }),
    'apply'
  )
})
