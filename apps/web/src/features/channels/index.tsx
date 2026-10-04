import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema, PageData } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI, useAction, fields, text } from '@/features/foundation/hooks'
import {
  PageTitle,
  QueryState,
  Notices,
  Field,
  Empty,
} from '@/features/foundation/ui'

export function Channels() {
  const query = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100'
  )
  return (
    <>
      <PageTitle
        title='通道管理'
        description='通道编号和身份永久保留。当前版本支持槽位命名，摄像头配置将在 M1-B 接入。'
      />
      <QueryState
        pending={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      {query.data && (
        <div className='grid gap-4'>
          {query.data.items.length === 0 && (
            <Empty>没有已授权的通道。请联系管理员分配权限。</Empty>
          )}
          {query.data.items.map((channel) => (
            <ChannelRow
              key={`${channel.id}:${channel.version}`}
              channel={channel}
            />
          ))}
        </div>
      )}
    </>
  )
}
function ChannelRow({ channel }: { channel: Schema<'Channel'> }) {
  const action = useAction()
  const editable = channel.permissions.includes('configure')
  return (
    <article
      data-testid='channel-row'
      className='rounded-xl border bg-card p-5'
    >
      <div className='mb-3 flex items-center gap-4'>
        <span className='font-mono text-sm text-primary'>
          CH{String(channel.channel_no).padStart(2, '0')}
        </span>
        <h2 className='font-medium'>{channel.channel_name}</h2>
        <span className='ml-auto text-xs text-muted-foreground'>
          未配置摄像头
        </span>
      </div>
      <p className='mb-3 text-xs text-muted-foreground'>
        权限：{channel.permissions.join(' / ') || '无'}
      </p>
      <Notices {...action} />
      {editable && (
        <form
          className='flex items-end gap-3'
          onSubmit={(event) => {
            event.preventDefault()
            const data = fields(event.currentTarget)
            void action.run(
              () =>
                apiRequest(
                  `/api/v1/channels/${channel.id}`,
                  jsonRequest(
                    'PATCH',
                    { channel_name: text(data, 'channel_name') },
                    channel.version
                  )
                ),
              '名称已保存'
            )
          }}
        >
          <Field
            label='通道名称'
            name='channel_name'
            id={`name-${channel.id}`}
            defaultValue={channel.channel_name}
            maxLength={128}
            required
          />
          <Button disabled={action.pending}>保存名称</Button>
        </form>
      )}
    </article>
  )
}
