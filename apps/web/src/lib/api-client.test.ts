import { afterEach, expect, test, vi } from 'vitest'
import { apiRequest, ApiError, clearAuthentication } from './api-client'

afterEach(() => {
  vi.unstubAllGlobals()
  clearAuthentication()
})
test('a 401 clears CSRF and announces session invalidation', async () => {
  const invalidated = vi.fn()
  window.addEventListener('one-nvr:unauthenticated', invalidated)
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            error: { code: 'unauthenticated', message: '请重新登录' },
          }),
          { status: 401, headers: { 'Content-Type': 'application/json' } }
        )
    )
  )
  await expect(apiRequest('/api/v1/site')).rejects.toBeInstanceOf(ApiError)
  expect(invalidated).toHaveBeenCalledOnce()
  window.removeEventListener('one-nvr:unauthenticated', invalidated)
})
test('late replies from a revoked session cannot repopulate data', async () => {
  let complete: (response: Response) => void = () => {}
  vi.stubGlobal(
    'fetch',
    vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          complete = resolve
        })
    )
  )
  const request = apiRequest('/api/v1/channels')
  const assertion = expect(request).rejects.toThrow('会话已改变')
  clearAuthentication()
  complete(
    new Response(
      JSON.stringify({ data: { items: [{ channel_name: '旧会话内容' }] } })
    )
  )
  await assertion
})
test('errors preserve code and request id without raw response data', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            error: { code: 'version_conflict', message: '配置已更新' },
            request_id: 'safe-id',
            upstream: 'secret',
          }),
          { status: 409 }
        )
    )
  )
  try {
    await apiRequest('/api/v1/site')
    throw new Error('accepted failure')
  } catch (error) {
    expect(error).toMatchObject({
      status: 409,
      code: 'version_conflict',
      requestId: 'safe-id',
    })
    expect(String(error)).not.toContain('secret')
  }
})
