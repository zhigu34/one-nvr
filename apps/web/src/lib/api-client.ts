// Sessions remain in the server's HttpOnly cookie. CSRF lives only in memory.
//
// There are TWO tokens and they are not interchangeable:
//
//   csrfToken     — derived from the session cookie; what `protected` verifies.
//   preAuthToken  — issued by setup/status for the pre-auth cookie; what
//                   `anonymous` verifies (POST /setup and POST /auth/login).
//
// Collapsing them was the bug in both directions. Using the pre-auth token for
// protected writes made every mutation after a page load fail with
// csrf_rejected; discarding it entirely broke SIGN-IN, because /auth/login is an
// anonymous route and has nothing else to verify against.
let csrfToken = ''
let preAuthToken = ''
let generation = 0
// getRandomValues is available on ordinary HTTP as well as HTTPS. randomUUID
// requires a secure context and is absent on a LAN HTTP deployment.
export function newRequestKey() {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, (byte) =>
    byte.toString(16).padStart(2, '0')
  ).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public requestId = '',
    /**
     * Structured evidence the server attached to the failure. Archive probes put
     * the failing stage and the HTTP status here ("write", "HTTP 409"), which is
     * the difference between "connection failed" and knowing what to change.
     */
    public fields: Record<string, string> = {}
  ) {
    super(message)
  }
}
export function clearAuthentication() {
  csrfToken = ''
  preAuthToken = ''
  generation++
}

// The anonymous endpoints that verify the PRE-auth token instead of the session's.
// Both are reachable before anyone is signed in, which is why they cannot use a
// session-derived value.
const anonymousPaths = ['/api/v1/setup', '/api/v1/auth/login']

function isAnonymousPath(path: string): boolean {
  return anonymousPaths.some((prefix) => path === prefix || path.startsWith(prefix + '/'))
}

/**
 * loadPreAuthCSRF fetches the pre-auth token for sign-in.
 *
 * setup/status issues it together with the pre-auth cookie, and both are needed
 * by `anonymous`. Cached per page load: the token is valid for ten minutes and
 * regenerating it on every attempt would invalidate the cookie that must match
 * it.
 */
async function loadPreAuthCSRF() {
  if (preAuthToken) return
  await apiRequest<{ csrf_token: string }>('/api/v1/setup/status').catch(() => {})
}

/**
 * loadSessionCSRF obtains the CSRF token for the CURRENT session.
 *
 * `/auth/me` is the source: it is a protected GET, needs no CSRF of its own, and
 * its response carries the token derived from the session cookie. setup/status
 * cannot serve here — its token is signed for the pre-auth cookie, which login
 * deletes, so `protected` would reject every later write.
 *
 * A 401 is left to the caller: the write fails the same way, and the interceptor
 * turns it into the unauthenticated path.
 */
async function loadSessionCSRF() {
  if (csrfToken) return
  await apiRequest<{ csrf_token: string }>('/api/v1/auth/me').catch(() => {})
}
export async function apiRequest<T>(
  path: string,
  init: RequestInit = {},
  csrfRetried = false
): Promise<T> {
  if (!path.startsWith('/api/v1/') || path.includes('://'))
    throw new ApiError(422, 'invalid_path', 'API 地址无效')
  const started = generation
  const headers = new Headers(init.headers)
  const method = (init.method || 'GET').toUpperCase()
  // Sign-in and first-run setup verify the PRE-auth token; everything else
  // verifies the session's. Picking the wrong one is a 403 with no explanation,
  // which is how both directions of this bug presented.
  const anonymous = isAnonymousPath(path)
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    if (anonymous) await loadPreAuthCSRF()
    else await loadSessionCSRF()
    headers.set('X-CSRF-Token', anonymous ? preAuthToken : csrfToken)
    if (init.body instanceof FormData) headers.delete('Content-Type')
    else if (init.body) headers.set('Content-Type', 'application/json')
  }
  const response = await fetch(path, {
    ...init,
    headers,
    credentials: 'same-origin',
    cache: 'no-store',
  })
  if (generation !== started)
    throw new ApiError(401, 'session_changed', '会话已改变，请重新加载')
  let envelope: {
    data?: T
    error?: { code?: string; message?: string; fields?: Record<string, string> }
    request_id?: string
  }
  try {
    envelope = await response.json()
  } catch {
    throw new ApiError(
      response.status,
      'invalid_response',
      '服务响应无效，请稍后重试'
    )
  }
  if (generation !== started)
    throw new ApiError(401, 'session_changed', '会话已改变，请重新加载')
  if (!response.ok) {
    if (response.status === 401) {
      clearAuthentication()
      window.dispatchEvent(new Event('one-nvr:unauthenticated'))
    }
    // A 403 on a write is csrf_rejected in every path this API has (the other
    // 403, `forbidden`, is about the account's grants and retrying would just
    // fail again). Re-acquire the session's token and try once more: the token
    // is in memory only, so a reload, a second tab logging in, or a server that
    // rotated the cookie leaves this page holding one that will never verify.
    // Without this the operator sees a bare "Failed to load resource" and the
    // heartbeat that hit it simply stops renewing the lease.
    if (
      response.status === 403 &&
      envelope.error?.code === 'csrf_rejected' &&
      !csrfRetried &&
      !['GET', 'HEAD', 'OPTIONS'].includes(method)
    ) {
      // Drop only the token this request used, then try once.
      if (anonymous) preAuthToken = ''
      else csrfToken = ''
      return apiRequest<T>(path, init, true)
    }
    throw new ApiError(
      response.status,
      envelope.error?.code || 'service_error',
      envelope.error?.message || '服务返回了一个未说明原因的失败',
      envelope.request_id,
      envelope.error?.fields || {}
    )
  }
  const data = envelope.data
  // File each token under the authority it actually carries.
  //
  // setup/status issues the PRE-auth token (ten minutes, bound to the pre-auth
  // cookie that login deletes); /auth/me, /auth/login and friends issue the
  // session token. Storing either one in the other's slot is what produced both
  // halves of this bug: protected writes rejected after a page load, and sign-in
  // rejected outright once the pre-auth token was thrown away.
  if (data && typeof data === 'object' && 'csrf_token' in data && typeof data.csrf_token === 'string') {
    if (path === '/api/v1/setup/status') preAuthToken = data.csrf_token
    else csrfToken = data.csrf_token
  }
  return data as T
}
export function jsonRequest(
  method: string,
  body?: unknown,
  version?: number
): RequestInit {
  return {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
    headers: version === undefined ? {} : { 'If-Match': `"${version}"` },
  }
}
