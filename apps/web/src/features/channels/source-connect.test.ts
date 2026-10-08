import { afterEach, expect, test, vi } from 'vitest'
import { connectSource } from './source-connect'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => vi.clearAllMocks())
const context = { action: 'connect' as const, version: 2, progress: vi.fn() }
function fixture(options: { expires?: string; failed?: boolean } = {}) {
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path.endsWith('/test'))
      return { test_id: 'proof', job_id: 'test-job', state: 'queued' }
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
    if (path.endsWith('/source/status'))
      return {
        version: 2,
        current_revision_id: 'revision',
        storage_pool_id: 'pool',
        requires_initial_recording_mode: false,
      }
    if (path.includes('/jobs/')) return { state: 'succeeded' }
    if (path.endsWith('/source/apply') && init?.method === 'POST')
      return { job_id: 'apply-job' }
    throw new Error('unexpected fixture path ' + path)
  })
}
function connect(onTest = vi.fn()) {
  return connectSource(
    'channel',
    'revision',
    new AbortController().signal,
    context,
    onTest
  )
}
test('a failed main-stream test never applies or reports a successful connection', async () => {
  fixture({ failed: true })
  await expect(connect()).rejects.toThrow('检查摄像头账号')
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
  await expect(connect()).rejects.toThrow('有效视频画面')
  expect(
    calls.request.mock.calls.some(([path]) => path.endsWith('/source/apply'))
  ).toBe(false)
})
test('connecting never probes or reads the recording policy', async () => {
  fixture()
  await expect(connect()).resolves.toBe('摄像头已连接')
  expect(
    calls.request.mock.calls.some(
      ([path]) =>
        path.includes('/storage-pools/') || path.endsWith('/recording-policy')
    )
  ).toBe(false)
})
