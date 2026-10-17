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
test('the panel offers one action: prove the stream', async () => {
  fixture()
  const view = await render(<SourceTestControls {...base} proof={proof} />)
  await userEvent.click(
    view.getByRole('button', { name: '测试取流', exact: true })
  )
  expect(calls.request.mock.calls[0][0]).toContain(
    `/source-revisions/${base.revisionId}/test`
  )
  await expect.element(view.getByText('测试已排队')).toBeVisible()
  // Applying is the form's own "保存并启用" action; a second apply button here
  // duplicated it, so this panel must not offer one.
  expect(
    view.getByRole('button', { name: '测试并启用', exact: true }).elements()
  ).toHaveLength(0)
})

test('a recorded proof is reported with its evidence and freshness', async () => {
  fixture()
  const view = await render(<SourceTestControls {...base} proof={proof} />)
  // The panel's job is to tell the operator what the last test proved, so the
  // per-stream result and the overall verdict are on screen.
  await expect
    .element(view.getByText(/测试状态：/))
    .toBeVisible()
})

test('an expired proof is shown as stale rather than silently reused', async () => {
  fixture()
  const view = await render(
    <SourceTestControls
      {...base}
      proof={{ ...proof, expires_at: new Date(Date.now() - 1000).toISOString() }}
    />
  )
  await expect
    .element(view.getByText(/已过期或不属于当前修订/))
    .toBeVisible()
})
