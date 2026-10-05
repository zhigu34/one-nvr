import { apiRequest, jsonRequest } from '@/lib/api-client'

export function sourceCommand<T>(
  path: string,
  body: unknown,
  version?: number,
  signal?: AbortSignal,
  method = 'POST'
) {
  const init = jsonRequest(method, body, version)
  return apiRequest<T>(path, {
    ...init,
    headers: { ...init.headers, 'Idempotency-Key': crypto.randomUUID() },
    signal,
  })
}
export function downloadLocal(filename: string, content: Blob) {
  const url = URL.createObjectURL(content),
    link = document.createElement('a')
  try {
    link.href = url
    link.download = filename
    link.click()
  } finally {
    URL.revokeObjectURL(url)
  }
}
