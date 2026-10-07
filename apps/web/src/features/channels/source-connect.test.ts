import { afterEach, expect, test, vi } from 'vitest'
import { connectSource } from './source-connect'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => vi.clearAllMocks())
const context = { action: 'connect' as const, version: 2, progress: vi.fn() }
function fixture(
  options: { expires?: string; failed?: boolean; poolFailed?: boolean } = {}
) {
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path.endsWith('/test'))
      return { test_id: 'proof', job_id: 'pool-job', state: 'queued' }
    if (path.includes('/source-tests/'))
      return {
        id: 'proof',
        revision_id: 'revision',
        state: options.failed ? 'failed' : 'succeeded',
        main: { state: 'healthy', first_frame: true },
        sub: { state: 'healthy' },
        expires_at:
          options.expires || new Date(Date.now() + 60000).toISOString(),
      }
    if (path.endsWith('/recording-policy'))
      return { mode: options.poolFailed ? 'continuous' : 'none' }
    if (path.endsWith('/source/status'))
      return {
        version: 2,
        current_revision_id: 'revision',
        storage_pool_id: 'pool',
        requires_initial_recording_mode: false,
      }
    if (path.includes('/jobs/'))
      return {
        state: options.poolFailed ? 'failed' : 'succeeded',
        error_code: 'storage_check_failed',
      }
    if (path.endsWith('/source/apply') && init?.method === 'POST')
      return { job_id: 'apply-job' }
    throw new Error('unexpected fixture path')
  })
}
test('a failed main-stream test never applies or reports a successful connection', async () => {
  fixture({ failed: true })
  await expect(
    connectSource(
      'channel',
      'revision',
      new AbortController().signal,
      context,
      true,
      vi.fn()
    )
  ).rejects.toThrow('检查摄像头账号')
  expect(
    calls.request.mock.calls.some(([path]) => path.endsWith('/source/apply'))
  ).toBe(false)
})
test('a late successful test response after leaving the channel cannot apply it', async () => {
  let finish: (value: unknown) => void = () => {}
  calls.request.mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve
      })
  )
  const controller = new AbortController(),
    accepted = vi.fn()
  const result = connectSource(
    'channel',
    'revision',
    controller.signal,
    context,
    true,
    accepted
  )
  const rejected = expect(result).rejects.toThrow()
  controller.abort()
  finish({ test_id: 'proof', job_id: 'test-job' })
  await rejected
  expect(accepted).not.toHaveBeenCalled()
  expect(calls.request).toHaveBeenCalledOnce()
})
test('an invalid proof expiration never allows activation', async () => {
  fixture({ expires: 'invalid' })
  await expect(
    connectSource(
      'channel',
      'revision',
      new AbortController().signal,
      context,
      true,
      vi.fn()
    )
  ).rejects.toThrow('有效视频画面')
  expect(
    calls.request.mock.calls.some(([path]) => path.endsWith('/source/apply'))
  ).toBe(false)
})
test('a failed recording pool check is explained as storage and preserves the current source', async () => {
  fixture({ poolFailed: true })
  await expect(
    connectSource(
      'channel',
      'revision',
      new AbortController().signal,
      context,
      true,
      vi.fn()
    )
  ).rejects.toThrow('录像存储池检查未通过')
  expect(
    calls.request.mock.calls.some(([path]) => path.endsWith('/source/apply'))
  ).toBe(false)
})
test('advanced save-and-test deliberately leaves the existing source unchanged', async () => {
  fixture()
  await expect(
    connectSource(
      'channel',
      'revision',
      new AbortController().signal,
      { ...context, action: 'test' },
      true,
      vi.fn()
    )
  ).resolves.toBe('配置已保存')
  expect(calls.request).toHaveBeenCalledOnce()
})
