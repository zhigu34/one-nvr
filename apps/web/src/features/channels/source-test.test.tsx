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
// A fresh test always answers with fresh evidence for the revision it was
// asked about; only the proof already on screen can be stale.
function fixture() {
  calls.request.mockImplementation(async (path: string) => {
    if (path.endsWith('/test'))
      return { job_id: 'test-job', state: 'queued', test_id: proof.id }
    if (path.includes('/source-tests/')) return proof
    if (path.endsWith('/source/apply'))
      return { job_id: 'apply-job', state: 'queued', test_id: proof.id }
    if (path.endsWith('/source/status'))
      return {
        channel_id: base.channelId,
        storage_pool_id: null,
        version: base.version,
      }
    if (path.startsWith('/api/v1/storage-pools'))
      return { items: [], next_cursor: null }
    throw new Error('unexpected ' + path)
  })
}
const enable = (view: Awaited<ReturnType<typeof render>>) =>
  view.getByRole('button', { name: '测试并启用', exact: true })

test('testing and enabling stay separate actions, and enabling submits the proof it holds', async () => {
  fixture()
  const view = await render(<SourceTestControls {...base} proof={proof} />)
  await userEvent.click(
    view.getByRole('button', { name: '测试取流', exact: true })
  )
  expect(calls.request.mock.calls[0][0]).toContain(
    `/source-revisions/${base.revisionId}/test`
  )
  await expect.element(view.getByText('测试已排队')).toBeVisible()
  calls.request.mockClear()
  await userEvent.click(enable(view))
  const [path, init] = calls.request.mock.calls[0]
  expect(path).toContain('/source/apply')
  expect(JSON.parse(init.body)).toEqual({
    revision_id: base.revisionId,
    test_id: proof.id,
  })
  expect(new Headers(init.headers).get('If-Match')).toBe('"8"')
})

test('a first apply starts stream-only and offers no recording choice here', async () => {
  fixture()
  const view = await render(
    <SourceTestControls {...base} first proof={proof} />
  )
  // Recording belongs to the recording plan page, so this control must not ask
  // for a mode and must not gate enablement on a storage pool.
  expect(view.getByLabelText('首次普通录像').elements()).toHaveLength(0)
  await userEvent.click(enable(view))
  expect(JSON.parse(calls.request.mock.calls[0][1].body)).toEqual({
    revision_id: base.revisionId,
    test_id: proof.id,
    first_recording_mode: 'none',
  })
})

test('enabling with no usable proof takes a fresh one first instead of refusing the click', async () => {
  fixture()
  const view = await render(<SourceTestControls {...base} proof={null} />)
  // The operator is never told to fetch evidence by hand: one click proves the
  // stream and then applies it.
  await expect.element(enable(view)).toBeEnabled()
  await userEvent.click(enable(view))
  const paths = calls.request.mock.calls.map(([path]) => path)
  expect(
    paths.some((path: string) => path.endsWith(`/${base.revisionId}/test`))
  ).toBe(true)
  const apply = calls.request.mock.calls.find(([path]) =>
    path.endsWith('/source/apply')
  )!
  expect(JSON.parse(apply[1].body)).toEqual({
    revision_id: base.revisionId,
    test_id: proof.id,
  })
  await expect
    .element(view.getByText('启用已排队，等待执行结果'))
    .toBeVisible()
})

test('an expired proof is re-taken, never submitted as if it were still valid', async () => {
  fixture()
  const view = await render(
    <SourceTestControls {...base} proof={{ ...proof, expires_at: new Date(Date.now() - 1000).toISOString() }} />
  )
  await expect.element(view.getByText(/已过期/)).toBeVisible()
  await userEvent.click(enable(view))
  const paths = calls.request.mock.calls.map(([path]) => path)
  expect(
    paths.some((path: string) => path.endsWith(`/${base.revisionId}/test`))
  ).toBe(true)
  expect(paths.some((path: string) => path.endsWith('/source/apply'))).toBe(true)
})

test('a proof bound to another revision is re-taken rather than authorizing this one', async () => {
  fixture()
  const view = await render(
    <SourceTestControls {...base} proof={{ ...proof, revision_id: 'other' }} />
  )
  await userEvent.click(enable(view))
  // The foreign proof is never submitted: the click proves this revision first
  // and applies the proof that describes it.
  const apply = calls.request.mock.calls.find(([path]) =>
    path.endsWith('/source/apply')
  )!
  expect(JSON.parse(apply[1].body).revision_id).toBe(base.revisionId)
  const tests = calls.request.mock.calls.filter(([path]) =>
    path.endsWith(`/${base.revisionId}/test`)
  )
  expect(tests).toHaveLength(1)
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
  await userEvent.click(enable(view))
  await expect
    .element(view.getByText('启用已排队，等待执行结果'))
    .toBeVisible()
  expect(applies).toBe(2)
  expect(base.onAccepted).toHaveBeenCalledWith(
    expect.objectContaining({ job_id: 'apply-job' }),
    'apply'
  )
})
