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
  /**
   * `message` may be a function, evaluated AFTER the action settles.
   *
   * Some actions can only describe their outcome once they have run — a batch
   * delete returns a per-row verdict ("已删除，释放 X" or "未删除：lease_held").
   * Callers that passed a function had it converted to a string at call time by
   * a local `let`, so the notice was written as the empty string and the button
   * appeared to do nothing at all. Accepting the function here removes that
   * trap: the string is produced at the moment it is known.
   */
  async function run(
    action: () => Promise<unknown>,
    message: string | (() => string),
    refresh = true
  ) {
    if (pending) return false
    setPending(true)
    setError('')
    setNotice('')
    try {
      await action()
      const text = typeof message === 'function' ? message() : message
      if (notify && text) notifySuccess(text)
      else setNotice(text)
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
