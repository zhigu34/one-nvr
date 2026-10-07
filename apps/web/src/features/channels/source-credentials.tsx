import { useEffect, useRef, useState } from 'react'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Field, Notices } from '@/features/foundation/ui'

type Props = { channelId: string; revisionId: string; allowed: boolean }
export function CredentialReveal(props: Props) {
  return (
    <Reveal
      key={`${props.channelId}:${props.revisionId}:${props.allowed}`}
      {...props}
    />
  )
}
function Reveal({ channelId, revisionId, allowed }: Props) {
  const [value, setValue] = useState<Schema<'RevealedCredentials'> | null>(
      null
    ),
    [pending, setPending] = useState(false),
    [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => {
    const hide = () => {
      controller.current?.abort()
      setValue(null)
      setPending(false)
    }
    window.addEventListener('one-nvr:unauthenticated', hide)
    window.addEventListener('one-nvr:protocol-changed', hide)
    return () => {
      controller.current?.abort()
      window.removeEventListener('one-nvr:unauthenticated', hide)
      window.removeEventListener('one-nvr:protocol-changed', hide)
    }
  }, [])
  if (!allowed) return null
  const hide = () => {
    controller.current?.abort()
    setValue(null)
    setPending(false)
    setError('')
  }
  return (
    <section className='grid gap-3'>
      <Notices error={error} notice='' />
      {value || pending ? (
        <Button type='button' variant='outline' onClick={hide}>
          隐藏账号密码
        </Button>
      ) : (
        <Button
          type='button'
          variant='outline'
          onClick={async () => {
            const abort = new AbortController()
            controller.current = abort
            setPending(true)
            setError('')
            try {
              const credentials = await apiRequest<
                Schema<'RevealedCredentials'>
              >(`/api/v1/channels/${channelId}/source/credentials/reveal`, {
                ...jsonRequest('POST', { revision_id: revisionId }),
                signal: abort.signal,
              })
              if (!abort.signal.aborted) setValue(credentials)
            } catch (e) {
              if (!abort.signal.aborted) {
                setValue(null)
                setError(e instanceof Error ? e.message : '读取失败')
              }
            } finally {
              if (!abort.signal.aborted) setPending(false)
            }
          }}
        >
          查看账号密码
        </Button>
      )}
      {value && (
        <>
          <Field
            label='已保存用户名'
            name='revealed_username'
            value={value.username}
            readOnly
            autoComplete='off'
          />
          <Field
            label='已保存密码'
            name='revealed_password'
            value={value.password}
            readOnly
            autoComplete='off'
          />
          <p className='text-xs text-muted-foreground'>
            仅在此处临时显示，隐藏或离开页面后清除。
          </p>
        </>
      )}
    </section>
  )
}
