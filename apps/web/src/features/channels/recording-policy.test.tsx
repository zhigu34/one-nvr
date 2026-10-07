import { afterEach, expect, test, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import type { Schema } from '@/lib/types'
import { RecordingPolicyControls } from './recording-policy'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => vi.clearAllMocks())
const channel: Schema<'Channel'> = {
  id: '00000000-0000-4000-8000-000000000001',
  channel_no: 1,
  channel_name: '门',
  permissions: ['configure'],
  version: 9,
}
test('missing pool blocks recording start and stopping preserves current source', async () => {
  calls.request.mockResolvedValue({ job_id: 'job', state: 'queued' })
  const view = await render(
    <RecordingPolicyControls
      channel={channel}
      mode='none'
      poolId={null}
      pools={[]}
      onAccepted={vi.fn()}
    />
  )
  await userEvent.selectOptions(
    view.getByLabelText('普通录像', { exact: true }),
    'continuous'
  )
  await expect
    .element(view.getByRole('button', { name: '保存录像策略', exact: true }))
    .toBeDisabled()
  await view.rerender(
    <RecordingPolicyControls
      channel={channel}
      mode='continuous'
      poolId={null}
      pools={[]}
      onAccepted={vi.fn()}
    />
  )
  await userEvent.selectOptions(
    view.getByLabelText('普通录像', { exact: true }),
    'none'
  )
  await userEvent.click(
    view.getByRole('button', { name: '保存录像策略', exact: true })
  )
  expect(calls.request.mock.calls[0][0]).toContain('/recording-policy')
  expect(calls.request.mock.calls[0][1].method).toBe('PUT')
  expect(JSON.parse(calls.request.mock.calls[0][1].body)).toEqual({
    mode: 'none',
  })
  await expect
    .element(
      view.getByText('关闭录像后仍可取流，已有录像保留。', { exact: false })
    )
    .toBeVisible()
})
