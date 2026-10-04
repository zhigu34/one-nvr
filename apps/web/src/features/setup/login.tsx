import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest, clearAuthentication } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { fields, text, useAction } from '@/features/foundation/hooks'
import { Field, Notices } from '@/features/foundation/ui'

export function Login() {
  const navigate = useNavigate()
  const client = useQueryClient()
  const action = useAction()
  return (
    <main className='mx-auto flex min-h-svh max-w-md flex-col justify-center p-6'>
      <div className='rounded-2xl border bg-card p-8'>
        <p className='mb-2 text-sm text-muted-foreground'>one-nvr · 本地账号</p>
        <h1 className='mb-6 text-2xl font-semibold'>登录</h1>
        <Notices {...action} />
        <form
          className='grid gap-4'
          onSubmit={(event) => {
            event.preventDefault()
            const data = fields(event.currentTarget)
            void action.run(
              async () => {
                clearAuthentication()
                await client.cancelQueries()
                client.clear()
                await apiRequest('/api/v1/setup/status')
                const session = await apiRequest<Schema<'Session'>>(
                  '/api/v1/auth/login',
                  jsonRequest('POST', {
                    username: text(data, 'username'),
                    password: text(data, 'password'),
                  } satisfies Schema<'LoginInput'>)
                )
                useAuthStore.getState().setUser(session.user)
                await navigate({ to: '/', replace: true })
              },
              '',
              false
            )
          }}
        >
          <Field
            label='用户名'
            name='username'
            autoComplete='username'
            required
          />
          <Field
            label='密码'
            name='password'
            type='password'
            autoComplete='current-password'
            required
          />
          <Button disabled={action.pending}>登录</Button>
        </form>
      </div>
    </main>
  )
}
