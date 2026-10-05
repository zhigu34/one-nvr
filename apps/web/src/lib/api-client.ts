// Sessions remain in the server's HttpOnly cookie. CSRF lives only in memory.
let csrfToken = ''
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
    public requestId = ''
  ) {
    super(message)
  }
}
export function clearAuthentication() {
  csrfToken = ''
  generation++
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
    if (!csrfToken)
      await apiRequest<{ csrf_token: string }>('/api/v1/setup/status')
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
    error?: { code?: string; message?: string }
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
      envelope.error?.message || '服务暂时不可用',
      envelope.request_id
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
