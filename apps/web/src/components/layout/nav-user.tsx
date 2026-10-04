import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest, clearAuthentication } from '@/lib/api-client'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useAction, fields } from '@/features/foundation/hooks'
import { Notices, Field } from '@/features/foundation/ui'

export function NavUser() {
  const user = useAuthStore((s) => s.user)
  const navigate = useNavigate()
  const client = useQueryClient()
  const action = useAction()
  const [passwordOpen, setPasswordOpen] = useState(false)
  return (
    <div className='p-2'>
      <p className='mb-2 truncate text-sm'>
        {user?.username} · {user?.role}
      </p>
      <Notices error={action.error} notice='' />
      <Button
        variant='outline'
        className='mb-2 w-full'
        onClick={() => setPasswordOpen(true)}
      >
        修改密码
      </Button>
      <Dialog open={passwordOpen} onOpenChange={setPasswordOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>修改密码</DialogTitle>
          </DialogHeader>
          <Notices error={action.error} notice='' />
          <form
            className='grid gap-4'
            onSubmit={(event) => {
              event.preventDefault()
              const form = event.currentTarget
              const data = fields(form)
              void action.run(
                async () => {
                  await apiRequest(
                    '/api/v1/auth/change-password',
                    jsonRequest('POST', {
                      old_password: data.get('old_password'),
                      new_password: data.get('new_password'),
                    })
                  )
                  form.reset()
                  setPasswordOpen(false)
                  clearAuthentication()
                  useAuthStore.getState().reset()
                  await client.cancelQueries()
                  client.clear()
                  await navigate({ to: '/sign-in', replace: true })
                },
                '',
                false
              )
            }}
          >
            <Field
              label='当前密码'
              name='old_password'
              type='password'
              autoComplete='current-password'
              required
            />
            <Field
              label='新密码'
              name='new_password'
              type='password'
              minLength={12}
              autoComplete='new-password'
              required
            />
            <p className='text-sm text-muted-foreground'>
              保存后所有登录会话失效，请使用新密码登录。
            </p>
            <Button disabled={action.pending}>保存新密码</Button>
          </form>
        </DialogContent>
      </Dialog>
      <Button
        variant='outline'
        className='w-full'
        disabled={action.pending}
        onClick={() =>
          void action.run(
            async () => {
              await apiRequest('/api/v1/auth/logout', jsonRequest('POST'))
              clearAuthentication()
              useAuthStore.getState().reset()
              await client.cancelQueries()
              client.clear()
              await navigate({ to: '/sign-in', replace: true })
            },
            '',
            false
          )
        }
      >
        退出登录
      </Button>
    </div>
  )
}
