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

test.each([200, 401])(
  'revocation during status %s body parsing discards data and CSRF',
  async (status) => {
    let finishBody: (body: unknown) => void = () => {}
    let bodyStarted: () => void = () => {}
    const parsing = new Promise<void>((resolve) => {
      bodyStarted = resolve
    })
    const fetchMock = vi.fn().mockResolvedValueOnce({
      ok: status === 200,
      status,
      json: () => {
        bodyStarted()
        return new Promise((resolve) => {
          finishBody = resolve
        })
      },
    })
    vi.stubGlobal('fetch', fetchMock)
    const request = apiRequest('/api/v1/auth/me')
    const assertion = expect(request).rejects.toThrow('会话已改变')
    await parsing
    clearAuthentication()
    finishBody({
      data: { username: 'retired-admin', csrf_token: 'retired-csrf' },
    })
    await assertion
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { csrf_token: 'fresh-csrf' } }))
    )
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ data: {} })))
    await apiRequest('/api/v1/site', { method: 'PATCH', body: '{}' })
    expect(fetchMock.mock.calls[1][0]).toBe('/api/v1/setup/status')
    expect(
      new Headers(fetchMock.mock.calls[2][1].headers).get('X-CSRF-Token')
    ).toBe('fresh-csrf')
  }
)

test('multipart keeps its browser boundary and CSRF instead of a JSON content type', async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { csrf_token: 'fixture-csrf' } }))
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { batch_id: 'fixture' } }))
    )
  vi.stubGlobal('fetch', fetchMock)
  const data = new FormData()
  data.append('format', 'csv')
  data.append('file', new File(['fixture'], 'camera.csv'))
  await apiRequest('/api/v1/source-imports/preview', {
    method: 'POST',
    body: data,
  })
  const headers = new Headers(fetchMock.mock.calls[1][1].headers)
  expect(headers.get('Content-Type')).toBeNull()
  expect(headers.get('X-CSRF-Token')).toBe('fixture-csrf')
})
