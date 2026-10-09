import { useNavigate } from '@tanstack/react-router'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { fields, text, useAction } from '@/features/foundation/hooks'
import { Field, SelectField, Notices } from '@/features/foundation/ui'

export function Setup() {
  const navigate = useNavigate()
  const action = useAction()
  return (
    <main className='mx-auto flex min-h-svh max-w-xl flex-col justify-center p-6'>
      <div className='rounded-2xl border bg-card p-8'>
        <p className='mb-2 text-sm text-muted-foreground'>one-nvr · 首次部署</p>
        <h1 className='mb-3 text-2xl font-semibold'>初始化 one-nvr</h1>
        <p className='mb-6 text-sm text-muted-foreground'>
          使用 deploy.sh 提供的初始化令牌创建管理员和固定通道。
        </p>
        <Notices {...action} />
        <form
          className='grid gap-4'
          onSubmit={(event) => {
            event.preventDefault()
            const data = fields(event.currentTarget)
            void action.run(
              async () => {
                const body = {
                  token: text(data, 'token'),
                  admin_name: text(data, 'admin_name'),
                  admin_password: text(data, 'admin_password'),
                  name: text(data, 'name'),
                  timezone: text(data, 'timezone'),
                  channel_count: text(data, 'channel_count') === '32' ? 32 : 16,
                } satisfies Schema<'SetupInput'>
                await apiRequest('/api/v1/setup', jsonRequest('POST', body))
                await navigate({ to: '/sign-in', replace: true })
              },
              '',
              false
            )
          }}
        >
          <Field
            label='初始化令牌'
            name='token'
            type='password'
            autoComplete='off'
            required
          />
          <Field
            label='站点名称'
            name='name'
            defaultValue='我的站点'
            maxLength={128}
            required
          />
          <Field
            label='管理员用户名'
            name='admin_name'
            autoComplete='username'
            defaultValue='admin'
            required
          />
          <Field
            label='管理员密码'
            name='admin_password'
            type='password'
            autoComplete='new-password'
            minLength={12}
            maxLength={128}
            required
          />
          <SelectField
            label='通道数量'
            name='channel_count'
            defaultValue='16'
            options={[
              { value: '16', label: '16 路' },
              { value: '32', label: '32 路' },
            ]}
          />
          <SelectField
            label='初始时区'
            name='timezone'
            defaultValue='Asia/Shanghai'
            options={[
              {
                value: 'Asia/Shanghai',
                label: 'Asia/Shanghai · 中国标准时间',
              },
              { value: 'UTC', label: 'UTC' },
            ]}
          />
          <p className='text-xs text-muted-foreground'>
            之后可在系统设置选择全部 IANA
            时区。摄像头配置与录制功能将在后续版本接入。
          </p>
          <Button disabled={action.pending}>创建站点</Button>
        </form>
      </div>
    </main>
  )
}
