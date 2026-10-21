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
    // The bootstrap must ask the SESSION for its token. setup/status issues a
    // pre-auth token, which `protected` rejects — every write after a page load
    // used to fail with 403 csrf_rejected because of it.
    expect(fetchMock.mock.calls[1][0]).toBe('/api/v1/auth/me')
    expect(
      new Headers(fetchMock.mock.calls[2][1].headers).get('X-CSRF-Token')
    ).toBe('fresh-csrf')
  }
)

// Every write after a page load needs the SESSION's CSRF token. The bootstrap
// used to call setup/status, which issues a pre-auth token signed for a cookie
// login deletes — `protected` rejected it, so the first write after a reload
// (the playback lease heartbeat, 30 seconds in) failed with 403 csrf_rejected.
test('a write with no cached token bootstraps from the session, not pre-auth', async () => {
  clearAuthentication()
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { user: {}, csrf_token: 'session-csrf' } }))
    )
    .mockResolvedValueOnce(new Response(JSON.stringify({ data: {} })))
  vi.stubGlobal('fetch', fetchMock)
  await apiRequest('/api/v1/recordings/seg-1/lease', { method: 'POST' })
  expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/auth/me')
  expect(
    new Headers(fetchMock.mock.calls[1][1].headers).get('X-CSRF-Token')
  ).toBe('session-csrf')
})

// A stale in-memory token must recover, not surface as a bare 403. The token
// lives only in this tab: a reload, a second tab logging in, or a rotated cookie
// leaves it invalid, and the write that used it (the playback lease heartbeat)
// would otherwise just stop working.
test('a csrf_rejected write re-acquires the token and retries once', async () => {
  clearAuthentication()
  const fetchMock = vi
    .fn()
    // 1. session token for the first attempt
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { user: {}, csrf_token: 'stale-csrf' } }))
    )
    // 2. the write is rejected
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({ error: { code: 'csrf_rejected', message: '请求来源或安全令牌无效' } }),
        { status: 403 }
      )
    )
    // 3. re-acquire
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { user: {}, csrf_token: 'fresh-csrf' } }))
    )
    // 4. the retry succeeds
    .mockResolvedValueOnce(new Response(JSON.stringify({ data: { renewed: true } })))
  vi.stubGlobal('fetch', fetchMock)
  await apiRequest('/api/v1/recordings/seg-1/lease', { method: 'POST' })
  expect(fetchMock).toHaveBeenCalledTimes(4)
  expect(new Headers(fetchMock.mock.calls[1][1].headers).get('X-CSRF-Token')).toBe('stale-csrf')
  expect(new Headers(fetchMock.mock.calls[3][1].headers).get('X-CSRF-Token')).toBe('fresh-csrf')
})

// A `forbidden` 403 is about the account's grants. Retrying cannot change it,
// and a silent second attempt would double every rejected write.
test('a forbidden write is not retried', async () => {
  clearAuthentication()
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { user: {}, csrf_token: 'csrf' } }))
    )
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({ error: { code: 'forbidden', message: '没有权限执行此操作' } }),
        { status: 403 }
      )
    )
  vi.stubGlobal('fetch', fetchMock)
  await expect(apiRequest('/api/v1/site', { method: 'PATCH', body: '{}' })).rejects.toThrow(
    '没有权限执行此操作'
  )
  expect(fetchMock).toHaveBeenCalledTimes(2)
})

// setup/status carries a PRE-auth token that `protected` can never verify. The
// authenticated route calls it on every entry, and adopting its token wiped the
// session token — every write in that tab then failed with csrf_rejected, which
// is exactly what the playback lease heartbeat kept reporting.
test('setup/status must not overwrite the session CSRF token', async () => {
  clearAuthentication()
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { user: {}, csrf_token: 'session-csrf' } }))
    )
    .mockResolvedValueOnce(new Response(JSON.stringify({ data: {} })))
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { initialized: true, csrf_token: 'preauth-csrf' } }))
    )
    .mockResolvedValueOnce(new Response(JSON.stringify({ data: {} })))
  vi.stubGlobal('fetch', fetchMock)
  await apiRequest('/api/v1/recordings/seg-1/lease', { method: 'POST' })
  await apiRequest('/api/v1/setup/status')
  await apiRequest('/api/v1/recordings/seg-1/lease', { method: 'POST' })
  expect(new Headers(fetchMock.mock.calls[1][1].headers).get('X-CSRF-Token')).toBe('session-csrf')
  // The write after setup/status still carries the SESSION token.
  expect(new Headers(fetchMock.mock.calls[3][1].headers).get('X-CSRF-Token')).toBe('session-csrf')
})

// Sign-in is an ANONYMOUS route: the server verifies the pre-auth token there,
// not the session's. Throwing the pre-auth token away (the previous fix) broke
// login entirely — POST /auth/login answered 403 csrf_rejected with no usable
// message, and nobody could get in.
test('login sends the pre-auth token, and the session token takes over after', async () => {
  clearAuthentication()
  const fetchMock = vi
    .fn()
    // 1. setup/status issues the pre-auth token
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { initialized: true, csrf_token: 'preauth-csrf' } }))
    )
    // 2. login succeeds and returns the session token
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ data: { user: {}, csrf_token: 'session-csrf' } }))
    )
    // 3. a write after login
    .mockResolvedValueOnce(new Response(JSON.stringify({ data: {} })))
  vi.stubGlobal('fetch', fetchMock)

  await apiRequest('/api/v1/setup/status')
  await apiRequest('/api/v1/auth/login', {
    method: 'POST',
    body: JSON.stringify({ username: 'admin', password: 'x' }),
  })
  expect(new Headers(fetchMock.mock.calls[1][1].headers).get('X-CSRF-Token')).toBe('preauth-csrf')

  await apiRequest('/api/v1/recordings/seg-1/lease', { method: 'POST' })
  // After login the session token is the one that verifies.
  expect(new Headers(fetchMock.mock.calls[2][1].headers).get('X-CSRF-Token')).toBe('session-csrf')
})

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
