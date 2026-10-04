import { useAuthStore } from '@/stores/auth-store'
import type { Schema, PageData } from '@/lib/types'
import { useAPI } from './hooks'
import { PageTitle, Panel, QueryState } from './ui'

export function Overview() {
  const user = useAuthStore((s) => s.user)
  const site = useAPI<Schema<'Site'>>('/api/v1/site')
  const channels = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100'
  )
  const components = useAPI<Schema<'Component'>[]>(
    '/api/v1/operations/components',
    user?.role === 'admin',
    5000
  )
  return (
    <>
      <PageTitle
        title='总览'
        description='站点配置和服务状态来自实际 API。在线率、录像完整率和事件统计将在对应业务接入后提供。'
      />
      <QueryState
        pending={site.isPending || channels.isPending}
        error={site.error || channels.error}
        retry={() => {
          void site.refetch()
          void channels.refetch()
        }}
      />
      {site.data && channels.data && (
        <div className='grid gap-4 md:grid-cols-3'>
          <Panel title='站点'>
            <p>{site.data.name}</p>
            <p className='mt-2 text-sm text-muted-foreground'>
              {site.data.timezone}
            </p>
          </Panel>
          <Panel title='固定通道'>
            <p className='text-3xl font-semibold'>{site.data.channel_count}</p>
            <p className='mt-2 text-sm text-muted-foreground'>
              当前授权 {channels.data.items.length} 路
            </p>
          </Panel>
          <Panel title='录像与事件'>
            <p className='text-muted-foreground'>尚未提供业务数据</p>
          </Panel>
        </div>
      )}
      {user?.role === 'admin' && (
        <Panel title='组件状态'>
          <QueryState
            pending={components.isPending}
            error={components.error}
            retry={() => components.refetch()}
          />
          <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-4'>
            {components.data?.map((c) => (
              <div key={c.name} className='rounded-lg border p-3'>
                <p className='font-medium'>{c.name}</p>
                <p className='mt-2 text-sm text-muted-foreground'>
                  {c.enabled ? c.state : '功能未启用'}
                </p>
                <p className='mt-1 text-xs break-all text-muted-foreground'>
                  {c.reason}
                </p>
              </div>
            ))}
          </div>
        </Panel>
      )}
    </>
  )
}
