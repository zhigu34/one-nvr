import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth-store'
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
import { downloadLocal, sourceCommand } from './api'
import { ChannelStatus } from './channel-status'
import { SourceImportControls } from './source-import-controls'

export function Channels({ initialBatch = '' }: { initialBatch?: string }) {
  const admin = useAuthStore((s) => s.user?.role === 'admin')
  const exportAction = useAction()
  const [showImport, setShowImport] = useState(!!initialBatch)
  const query = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100',
    true,
    5000
  )
  return (
    <>
      <PageTitle
        title='通道管理'
        description='通道编号和身份永久保留。修改或更换摄像头不会改变通道身份，配置先保存、测试，再显式应用。'
      />
      <QueryState
        pending={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      {admin && query.data && (
        <div className='mb-6 grid gap-4'>
          <Notices {...exportAction} />
          <div className='flex gap-3'>
            <Button
              type='button'
              variant='outline'
              onClick={() => setShowImport((v) => !v)}
            >
              批量导入
            </Button>
            <Button
              type='button'
              variant='outline'
              disabled={exportAction.pending}
              onClick={() =>
                void exportAction.run(
                  async () => {
                    const value = await sourceCommand<
                      Schema<'SourceExportFile'>
                    >('/api/v1/channels/source-config-export', {
                      channel_ids: query.data.items.map((item) => item.id),
                    })
                    downloadLocal(
                      'one-nvr-channels.json',
                      new Blob([JSON.stringify(value, null, 2)], {
                        type: 'application/json',
                      })
                    )
                  },
                  '配置已导出，文件包含明文账号密码，请妥善保存',
                  false
                )
              }
            >
              导出摄像头配置
            </Button>
          </div>
          {showImport && (
            <SourceImportControls
              key={initialBatch}
              channels={query.data.items}
              initialBatch={initialBatch}
            />
          )}
        </div>
      )}
      {query.data && (
        <div className='grid gap-4'>
          {query.data.items.length === 0 && (
            <Empty>没有已授权的通道。请联系管理员分配权限。</Empty>
          )}
          {query.data.items.map((channel) => (
            <ChannelRow key={channel.id} channel={channel} />
          ))}
        </div>
      )}
    </>
  )
}
function ChannelRow({ channel }: { channel: Schema<'Channel'> }) {
  const action = useAction()
  const status = useAPI<Schema<'ChannelSourceStatus'>>(
    `/api/v1/channels/${channel.id}/source/status`,
    true,
    5000
  )
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
          {status.data?.current_revision_id
            ? '已配置摄像头'
            : status.data
              ? '未配置摄像头'
              : '状态加载中'}
        </span>
      </div>
      <p className='mb-3 text-xs text-muted-foreground'>
        权限：{channel.permissions.join(' / ') || '无'}
      </p>
      {status.data && (
        <div className='mb-4'>
          <ChannelStatus value={status.data} />
        </div>
      )}
      {status.error && (
        <p role='alert' className='mb-3 text-xs'>
          {status.error.message}
        </p>
      )}
      {editable && (
        <Link
          to='/channels/configure'
          search={{ channel: channel.id }}
          className='mb-4 inline-block text-sm text-primary underline'
        >
          配置摄像头与录像
        </Link>
      )}
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
            key={channel.version}
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
