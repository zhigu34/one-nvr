import { useState } from 'react'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest, clearAuthentication } from '@/lib/api-client'
import type { Schema, PageData } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI, useAction, fields, text } from '@/features/foundation/hooks'
import {
  PageTitle,
  Panel,
  Field,
  Notices,
  QueryState,
  Forbidden,
  Empty,
} from '@/features/foundation/ui'

const actions = [
  ['live', '实时'],
  ['playback', '回放'],
  ['export', '导出'],
  ['configure', '配置'],
] as const
export function Users() {
  const current = useAuthStore((s) => s.user)
  const admin = current?.role === 'admin'
  const [cursor, setCursor] = useState(''),
    [selected, setSelected] = useState('')
  const query = useAPI<PageData<Schema<'User'>>>(
    `/api/v1/users?limit=100${cursor ? '&cursor=' + cursor : ''}`,
    admin
  )
  const channels = useAPI<Schema<'ChannelSlot'>[]>(
    '/api/v1/channel-slots',
    admin
  )
  const action = useAction()
  if (!admin) return <Forbidden />
  const user = query.data?.items.find((u) => u.id === selected)
  return (
    <>
      <PageTitle
        title='用户与权限'
        description='账号角色和通道授权分别管理。修改角色、停用或通道授权会撤销该账号已有会话。'
      />
      <Panel title='创建账号'>
        <Notices {...action} />
        <form
          className='grid gap-4 md:grid-cols-4'
          onSubmit={(event) => {
            event.preventDefault()
            const form = event.currentTarget
            const data = fields(form)
            void action.run(async () => {
              await apiRequest(
                '/api/v1/users',
                jsonRequest('POST', {
                  username: text(data, 'username'),
                  password: text(data, 'password'),
                  role: text(data, 'role'),
                })
              )
              form.reset()
            }, '账号已创建')
          }}
        >
          <Field label='新用户名' name='username' autoComplete='off' required />
          <Field
            label='初始密码'
            name='password'
            type='password'
            autoComplete='new-password'
            minLength={12}
            required
          />
          <label className='grid gap-2'>
            角色
            <select
              aria-label='角色'
              name='role'
              className='rounded-md border bg-background p-2'
              defaultValue='viewer'
            >
              <option value='admin'>管理员</option>
              <option value='operator'>操作员</option>
              <option value='viewer'>查看者</option>
            </select>
          </label>
          <Button className='self-end' disabled={action.pending}>
            创建账号
          </Button>
        </form>
      </Panel>
      <QueryState
        pending={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      <div className='grid gap-3'>
        {query.data?.items.map((u) => (
          <UserRow
            key={`${u.id}:${u.version}`}
            user={u}
            onGrants={() => setSelected(u.id)}
          />
        ))}
      </div>
      {query.data?.next_cursor && (
        <Button
          className='mt-4'
          onClick={() => {
            setSelected('')
            setCursor(query.data!.next_cursor!)
          }}
        >
          下一页
        </Button>
      )}
      {cursor && (
        <Button
          className='m-4'
          variant='outline'
          onClick={() => {
            setSelected('')
            setCursor('')
          }}
        >
          返回首页
        </Button>
      )}
      {user && (
        <Panel title={`${user.username} 的通道权限`}>
          <QueryState
            pending={channels.isPending}
            error={channels.error}
            retry={() => channels.refetch()}
          />
          {channels.data && (
            <GrantEditor key={user.id} user={user} channels={channels.data} />
          )}
          <Button
            className='mt-4'
            variant='outline'
            onClick={() => setSelected('')}
          >
            关闭权限面板
          </Button>
        </Panel>
      )}
    </>
  )
}
function UserRow({
  user,
  onGrants,
}: {
  user: Schema<'User'>
  onGrants: () => void
}) {
  const current = useAuthStore((s) => s.user)
  const action = useAction()
  return (
    <article className='rounded-xl border bg-card p-4'>
      <div className='mb-3 flex items-center justify-between'>
        <span className='font-medium'>
          {user.username} · {user.enabled ? '已启用' : '已停用'}
        </span>
        <Button
          variant='outline'
          aria-label={`${user.username} 的通道权限`}
          onClick={onGrants}
        >
          通道权限
        </Button>
      </div>
      <Notices {...action} />
      <form
        className='flex flex-wrap items-center gap-3'
        onSubmit={(event) => {
          event.preventDefault()
          const data = fields(event.currentTarget)
          void action.run(async () => {
            await apiRequest(
              `/api/v1/users/${user.id}`,
              jsonRequest(
                'PATCH',
                { role: text(data, 'role'), enabled: data.has('enabled') },
                user.version
              )
            )
            if (current?.id === user.id) {
              clearAuthentication()
              window.dispatchEvent(new Event('one-nvr:unauthenticated'))
            }
          }, '账号已更新')
        }}
      >
        <label className='grid gap-2'>
          {user.username} 的角色
          <select
            aria-label={`${user.username} 的角色`}
            name='role'
            className='rounded-md border bg-background p-2'
            defaultValue={user.role}
          >
            <option value='admin'>管理员</option>
            <option value='operator'>操作员</option>
            <option value='viewer'>查看者</option>
          </select>
        </label>
        <label className='flex items-center gap-2'>
          <input type='checkbox' name='enabled' defaultChecked={user.enabled} />
          启用账号
        </label>
        <Button disabled={action.pending}>保存账号</Button>
      </form>
    </article>
  )
}
function GrantEditor({
  user,
  channels,
}: {
  user: Schema<'User'>
  channels: Schema<'ChannelSlot'>[]
}) {
  const current = useAuthStore((s) => s.user)
  const query = useAPI<Schema<'Grant'>[]>(
    `/api/v1/users/${user.id}/channel-grants`
  )
  const action = useAction()
  const [changes, setChanges] = useState<Record<string, boolean>>({})
  const checked = (cid: string, act: string) =>
    changes[cid + ':' + act] ??
    query.data?.some(
      (g) =>
        g.channel_id === cid &&
        g.actions.includes(act as Schema<'Grant'>['actions'][number])
    ) ??
    false
  return (
    <>
      <QueryState
        pending={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      <Notices {...action} />
      {query.data && (
        <form
          onSubmit={(event) => {
            event.preventDefault()
            const grants: Schema<'Grant'>[] = channels.map((c) => ({
              channel_id: c.id,
              actions: actions
                .filter(
                  ([act]) =>
                    checked(c.id, act) &&
                    (user.role !== 'viewer' || act !== 'configure')
                )
                .map(([act]) => act),
            }))
            void action.run(async () => {
              await apiRequest(
                `/api/v1/users/${user.id}/channel-grants`,
                jsonRequest('PUT', { grants }, user.version)
              )
              if (current?.id === user.id) {
                clearAuthentication()
                window.dispatchEvent(new Event('one-nvr:unauthenticated'))
              }
            }, '权限已保存')
          }}
        >
          {channels.length === 0 && <Empty>当前没有可显示的通道槽位。</Empty>}
          <div className='my-4 grid gap-3 md:grid-cols-2'>
            {channels.map((c) => (
              <fieldset key={c.id} className='rounded-lg border p-3'>
                <legend className='px-1 text-sm'>
                  CH{String(c.channel_no).padStart(2, '0')} · {c.channel_name}
                </legend>
                <div className='flex flex-wrap gap-4'>
                  {actions.map(([act, label]) => (
                    <label
                      key={act}
                      className='flex items-center gap-2 text-sm'
                    >
                      <input
                        aria-label={`CH${String(c.channel_no).padStart(2, '0')} ${label}`}
                        type='checkbox'
                        disabled={user.role === 'viewer' && act === 'configure'}
                        checked={
                          user.role === 'viewer' && act === 'configure'
                            ? false
                            : checked(c.id, act)
                        }
                        onChange={(event) =>
                          setChanges({
                            ...changes,
                            [c.id + ':' + act]: event.target.checked,
                          })
                        }
                      />
                      {label}
                    </label>
                  ))}
                </div>
              </fieldset>
            ))}
          </div>
          <Button disabled={action.pending}>保存通道权限</Button>
        </form>
      )}
    </>
  )
}
