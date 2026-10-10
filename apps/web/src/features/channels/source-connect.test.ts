import { afterEach, expect, test, vi } from 'vitest'
import { ApiError } from '@/lib/api-client'
import { connectSource } from './source-connect'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => vi.clearAllMocks())
const context = { action: 'connect' as const, version: 2, progress: vi.fn() }
function fixture(
  options: {
    expires?: string
    failed?: boolean
    retryApply?: boolean
    checkPending?: boolean
  } = {}
) {
  const attempts = { apply: 0 }
  calls.request.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path.includes('/storage-pools/') && path.endsWith('/test'))
      return { job_id: 'check-job' }
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
    if (path.startsWith('/api/v1/storage-pools'))
      return {
        items: [
          {
            id: 'pool',
            name: 'disk1',
            checks: [
              {
                service: 'zlm',
                state: options.checkPending ? 'pending' : 'healthy',
                reason: options.checkPending
                  ? 'test_source_required'
                  : 'zlm_media_verified',
                total_bytes: 0,
                free_bytes: 0,
                filesystem_id: '',
              },
            ],
          },
        ],
        next_cursor: null,
      }
    if (path.endsWith('/source/apply') && init?.method === 'POST') {
      attempts.apply++
      if (options.retryApply && attempts.apply === 1)
        throw new ApiError(
          409,
          'zlm_write_evidence_unavailable',
          '存储池检查未通过，请先运行存储池检查'
        )
      return { job_id: 'apply-job' }
    }
    throw new Error('unexpected fixture path ' + path)
  })
  return attempts
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
test('an apply rejected for stale pool evidence re-verifies the write and retries once', async () => {
  const attempts = fixture({ retryApply: true })
  await expect(connect()).resolves.toBe('摄像头已连接')
  expect(attempts.apply).toBe(2)
  const applies = calls.request.mock.calls.filter(([path]) =>
    path.endsWith('/source/apply')
  )
  // The retry is the same authorized apply, still carrying the version.
  expect(new Headers(applies[1][1].headers).get('If-Match')).toBe('"2"')
  expect(JSON.parse(applies[1][1].body)).toEqual({
    revision_id: 'revision',
    test_id: 'proof',
  })
  expect(
    calls.request.mock.calls.some(([path]) =>
      path.includes('/storage-pools/pool/test')
    )
  ).toBe(true)
  expect(context.progress).toHaveBeenCalledWith(
    '存储池写入检查已过期，正在自动重新验证…'
  )
})
test('a failed write re-verification reports the reason instead of retrying', async () => {
  const attempts = fixture({ retryApply: true, checkPending: true })
  await expect(connect()).rejects.toThrow(
    '需要先完成一路摄像头的连接测试或启用'
  )
  expect(attempts.apply).toBe(1)
})
