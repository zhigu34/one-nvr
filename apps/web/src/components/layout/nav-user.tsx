import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { ChevronsUpDown, KeyRound, LogOut } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest, clearAuthentication } from '@/lib/api-client'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'
import { useAction, fields } from '@/features/foundation/hooks'
import { Notices, Field } from '@/features/foundation/ui'

// The operator sees the words the application uses elsewhere, not the wire
// value.
const ROLE_LABELS: Record<string, string> = {
  admin: '管理员',
  operator: '操作员',
  viewer: '查看者',
}

// Account actions live behind the user card, the way the template presents
// them: avatar, name, role, and a menu instead of two loose buttons.
export function NavUser() {
  const user = useAuthStore((s) => s.user)
  const { isMobile } = useSidebar()
  const navigate = useNavigate()
  const client = useQueryClient()
  const action = useAction()
  const [passwordOpen, setPasswordOpen] = useState(false)
  const username = user?.username || '未登录'
  const role = ROLE_LABELS[user?.role || ''] || user?.role || ''
  const initials = username.slice(0, 1).toUpperCase()
  return (
    <>
      <SidebarMenu>
        <SidebarMenuItem>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <SidebarMenuButton
                size='lg'
                aria-label='用户菜单'
                className='data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground'
              >
                <Avatar className='size-8 rounded-lg'>
                  <AvatarFallback className='rounded-lg'>
                    {initials}
                  </AvatarFallback>
                </Avatar>
                <div className='grid flex-1 text-start text-sm leading-tight'>
                  <span className='truncate font-medium'>{username}</span>
                  <span className='truncate text-xs'>{role}</span>
                </div>
                <ChevronsUpDown className='ms-auto size-4' />
              </SidebarMenuButton>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              className='w-(--radix-dropdown-menu-trigger-width) min-w-56 rounded-lg'
              side={isMobile ? 'bottom' : 'right'}
              align='end'
              sideOffset={4}
            >
              <DropdownMenuLabel className='p-0 font-normal'>
                <div className='flex items-center gap-2 px-1 py-1.5 text-start text-sm'>
                  <Avatar className='size-8 rounded-lg'>
                    <AvatarFallback className='rounded-lg'>
                      {initials}
                    </AvatarFallback>
                  </Avatar>
                  <div className='grid flex-1 text-start text-sm leading-tight'>
                    <span className='truncate font-medium'>{username}</span>
                    <span className='truncate text-xs'>{role}</span>
                  </div>
                </div>
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuGroup>
                <DropdownMenuItem onSelect={() => setPasswordOpen(true)}>
                  <KeyRound aria-hidden='true' />
                  修改密码
                </DropdownMenuItem>
              </DropdownMenuGroup>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                onSelect={() =>
                  void action.run(
                    async () => {
                      await apiRequest(
                        '/api/v1/auth/logout',
                        jsonRequest('POST')
                      )
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
                <LogOut aria-hidden='true' />
                退出登录
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </SidebarMenuItem>
      </SidebarMenu>
      <Notices error={action.error} notice='' />
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
    </>
  )
}
