// Sessions remain in the server's HttpOnly cookie. CSRF lives only in memory.
let csrfToken = ''
// The token belongs to one session; a login or logout must drop it.
let sessionCSRF = ''
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
  sessionCSRF = ''
  generation++
}

/**
 * loadSessionCSRF obtains the CSRF token for the CURRENT session.
 *
 * It must not be setup/status. That endpoint issues a PRE-auth token, signed for
 * a cookie that login deletes, and `protected` compares against the session's own
 * token — so every write after a page load (where the in-memory token is empty)
 * was rejected with 403 csrf_rejected. That is exactly what the playback lease
 * heartbeat hit: 30 seconds after a reload, on a network where the request still
 * reached the server.
 *
 * `/auth/me` is the right source: it is a protected GET, needs no CSRF, and its
 * response carries the token derived from the session cookie. A 401 there is left
 * to the caller — the write will fail the same way, and the interceptor turns it
 * into the unauthenticated path.
 */
async function loadSessionCSRF(started: number) {
  if (sessionCSRF) {
    // Another request in this generation already fetched it.
    csrfToken = sessionCSRF
    return
  }
  await apiRequest<{ csrf_token: string }>('/api/v1/auth/me').catch(() => {})
  if (generation === started) sessionCSRF = csrfToken
}
export async function apiRequest<T>(
  path: string,
  init: RequestInit = {}
): Promise<T> {
  if (!path.startsWith('/api/v1/') || path.includes('://'))
    throw new ApiError(422, 'invalid_path', 'API 地址无效')
  const started = generation
  const headers = new Headers(init.headers)
  const method = (init.method || 'GET').toUpperCase()
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    if (!csrfToken) await loadSessionCSRF(started)
    headers.set('X-CSRF-Token', csrfToken)
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
    throw new ApiError(
      response.status,
      envelope.error?.code || 'service_error',
      envelope.error?.message || '服务返回了一个未说明原因的失败',
      envelope.request_id,
      envelope.error?.fields || {}
    )
  }
  const data = envelope.data
  if (
    data &&
    typeof data === 'object' &&
    'csrf_token' in data &&
    typeof data.csrf_token === 'string'
  )
    csrfToken = data.csrf_token
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
