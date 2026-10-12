import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { apiRequest } from '@/lib/api-client'
import { notifySuccess } from './notify'

export function useAPI<T>(
  path: string,
  enabled = true,
  interval: number | false = false
) {
  return useQuery({
    queryKey: [path],
    queryFn: ({ signal }) => apiRequest<T>(path, { signal }),
    enabled,
    refetchInterval: interval,
    retry: false,
  })
}
/**
 * `notify: true` reports success as a transient toast instead of the sticky
 * inline notice; errors stay on `error` either way so the form keeps its
 * context. Pages whose notices carry progress or are asserted inline stay on
 * the default.
 */
export function useAction(notify = false) {
  const client = useQueryClient()
  const [pending, setPending] = useState(false),
    [error, setError] = useState(''),
    [notice, setNotice] = useState('')
  async function run(
    action: () => Promise<unknown>,
    message: string,
    refresh = true
  ) {
    if (pending) return false
    setPending(true)
    setError('')
    setNotice('')
    try {
      await action()
      if (notify && message) notifySuccess(message)
      else setNotice(message)
      if (refresh) await client.invalidateQueries()
      return true
    } catch (e) {
      setError(e instanceof Error ? e.message : '操作失败')
      return false
    } finally {
      setPending(false)
    }
  }
  return { pending, error, notice, run }
}
export function fields(form: HTMLFormElement) {
  return new FormData(form)
}
export function text(data: FormData, name: string) {
  return String(data.get(name) || '')
}
