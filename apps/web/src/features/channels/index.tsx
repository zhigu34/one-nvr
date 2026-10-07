import { useState } from 'react'
import { useQueries } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Search } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest } from '@/lib/api-client'
import type { Schema, PageData } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useAPI, useAction } from '@/features/foundation/hooks'
import { PageTitle, QueryState, Notices, Empty } from '@/features/foundation/ui'
import { downloadLocal, sourceCommand } from './api'
import { StatusLabel } from './channel-status'
import { SourceImportControls } from './source-import-controls'

function numberLabel(channel: Schema<'Channel'>) {
  return `CH${String(channel.channel_no).padStart(2, '0')}`
}

export function Channels({ initialBatch = '' }: { initialBatch?: string }) {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const exportAction = useAction()
  const [showImport, setShowImport] = useState(!!initialBatch)
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState('all')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const query = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100',
    true,
    5000
  )
  const channels = query.data?.items || []
  const states = useQueries({
    queries: channels.map((channel) => ({
      queryKey: [`/api/v1/channels/${channel.id}/source/status`],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        apiRequest<Schema<'ChannelSourceStatus'>>(
          `/api/v1/channels/${channel.id}/source/status`,
          { signal }
        ),
      refetchInterval: 5000,
      retry: false,
    })),
  })
  const rows = channels.map((channel, index) => ({
    channel,
    status: states[index],
  }))
  const visible = rows.filter(({ channel, status }) => {
    const match =
      `${numberLabel(channel)} ${channel.channel_no} ${channel.channel_name}`
        .toLowerCase()
        .includes(search.toLowerCase().trim())
    if (!match) return false
    if (filter === 'all') return true
    if (!status.data) return false
    if (filter === 'configured') return !!status.data.current_revision_id
    if (filter === 'empty') return !status.data.current_revision_id
    return (['main', 'sub', 'recording'] as const).some((kind) =>
      ['unavailable', 'degraded'].includes(status.data![kind].state)
    )
  })
  const permitted = channels.filter((channel) =>
    channel.permissions.includes('configure')
  )
  const chosen = permitted.filter((channel) => selected.has(channel.id))
  const invalidSelection = selected.size > 0 && chosen.length !== selected.size
  const selectable = visible.filter(({ channel }) =>
    channel.permissions.includes('configure')
  )
  const allChecked =
    selectable.length > 0 &&
    selectable.every(({ channel }) => selected.has(channel.id))
  function select(id: string, checked: boolean) {
    setSelected((previous) => {
      const next = new Set(previous)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }
  return (
    <>
      <div className='mb-5 flex flex-wrap items-start justify-between gap-4'>
        <PageTitle
          title='通道管理'
          description='集中查看连接与录像状态，选择通道进行配置。'
        />
        {admin && query.data && (
          <div className='flex flex-wrap gap-2'>
            <Button
              type='button'
              variant='outline'
              disabled={
                exportAction.pending ||
                permitted.length === 0 ||
                invalidSelection
              }
              onClick={() =>
                void exportAction.run(
                  async () => {
                    const value = await sourceCommand<
                      Schema<'SourceExportFile'>
                    >('/api/v1/channels/source-config-export', {
                      channel_ids: (selected.size ? chosen : permitted).map(
                        (channel) => channel.id
                      ),
                    })
                    downloadLocal(
                      'one-nvr-channels.json',
                      new Blob([JSON.stringify(value, null, 2)], {
                        type: 'application/json',
                      })
                    )
                  },
                  '配置已导出，文件包含账号密码，请妥善保存',
                  false
                )
              }
            >
              导出摄像头配置
            </Button>
            <Button
              type='button'
              onClick={() => setShowImport((value) => !value)}
              aria-expanded={showImport}
            >
              批量导入
            </Button>
          </div>
        )}
      </div>
      <Notices {...exportAction} />
      <QueryState
        pending={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      {admin && query.data && showImport && (
        <div className='mb-5 rounded-xl border bg-card p-5'>
          <SourceImportControls
            key={initialBatch}
            channels={channels}
            initialBatch={initialBatch}
          />
        </div>
      )}
      {query.data && (
        <>
          <div className='mb-4 flex flex-wrap items-center justify-between gap-3'>
            <div className='flex flex-1 flex-wrap gap-3'>
              <div className='relative max-w-sm min-w-48 flex-1'>
                <Search
                  aria-hidden
                  className='absolute top-2.5 left-3 size-4 text-muted-foreground'
                />
                <Input
                  type='search'
                  aria-label='搜索通道'
                  placeholder='搜索通道编号或名称'
                  className='pl-9'
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                />
              </div>
              <select
                aria-label='配置状态'
                className='h-9 rounded-md border bg-background px-3 text-sm'
                value={filter}
                onChange={(event) => setFilter(event.target.value)}
              >
                <option value='all'>全部通道</option>
                <option value='configured'>已配置</option>
                <option value='empty'>未配置</option>
                <option value='abnormal'>异常通道</option>
              </select>
            </div>
            <span className='text-xs text-muted-foreground'>
              {visible.length} / {channels.length} 路
              {selected.size > 0 ? ` · 已选 ${selected.size} 路` : ''}
            </span>
          </div>
          {selected.size > 0 && (
            <div className='mb-3 flex items-center gap-3 text-sm'>
              <Button
                variant='ghost'
                size='sm'
                onClick={() => setSelected(new Set())}
              >
                取消选择
              </Button>
              {invalidSelection && (
                <span role='alert'>
                  部分所选通道已不可配置，请取消后重新选择。
                </span>
              )}
            </div>
          )}
          {channels.length === 0 ? (
            <Empty>没有已授权的通道。请联系管理员分配权限。</Empty>
          ) : (
            <div className='overflow-hidden rounded-xl border bg-card'>
              <Table>
                <TableHeader>
                  <TableRow>
                    {admin && (
                      <TableHead className='w-10'>
                        <input
                          type='checkbox'
                          aria-label='选择当前结果'
                          checked={allChecked}
                          disabled={!selectable.length}
                          onChange={(event) => {
                            const checked = event.target.checked
                            setSelected((previous) => {
                              const next = new Set(previous)
                              selectable.forEach(({ channel }) => {
                                if (checked) next.add(channel.id)
                                else next.delete(channel.id)
                              })
                              return next
                            })
                          }}
                        />
                      </TableHead>
                    )}
                    <TableHead>通道</TableHead>
                    <TableHead>名称</TableHead>
                    <TableHead>摄像头</TableHead>
                    <TableHead>主流</TableHead>
                    <TableHead>子流</TableHead>
                    <TableHead>录像</TableHead>
                    <TableHead className='text-right'>操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {visible.map(({ channel, status }) => (
                    <TableRow key={channel.id} data-testid='channel-row'>
                      {admin && (
                        <TableCell>
                          <input
                            type='checkbox'
                            aria-label={`选择 ${numberLabel(channel)}`}
                            checked={chosen.some(
                              (item) => item.id === channel.id
                            )}
                            disabled={
                              !channel.permissions.includes('configure')
                            }
                            onChange={(event) =>
                              select(channel.id, event.target.checked)
                            }
                          />
                        </TableCell>
                      )}
                      <TableCell className='font-mono text-xs'>
                        {numberLabel(channel)}
                      </TableCell>
                      <TableCell className='font-medium'>
                        {channel.channel_name}
                      </TableCell>
                      <TableCell className='text-xs text-muted-foreground'>
                        {status.error
                          ? '状态查询失败'
                          : status.data
                            ? status.data.current_revision_id
                              ? '已配置摄像头'
                              : '未配置摄像头'
                            : '读取中'}
                      </TableCell>
                      {(['main', 'sub', 'recording'] as const).map((kind) => (
                        <TableCell key={kind}>
                          {status.data ? (
                            <StatusLabel value={status.data[kind]} />
                          ) : (
                            <span className='text-xs text-muted-foreground'>
                              {status.error ? '未知' : '读取中'}
                            </span>
                          )}
                        </TableCell>
                      ))}
                      <TableCell className='text-right'>
                        {channel.permissions.includes('configure') ? (
                          <Link
                            to='/channels/configure'
                            search={{ channel: channel.id }}
                            aria-label={`配置 ${numberLabel(channel)}`}
                            className='inline-flex h-8 items-center rounded-md border px-3 text-xs font-medium hover:bg-accent'
                          >
                            配置
                          </Link>
                        ) : (
                          <span className='text-xs text-muted-foreground'>
                            只读
                          </span>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              {visible.length === 0 && (
                <div className='p-8 text-center text-sm text-muted-foreground'>
                  没有匹配的通道
                </div>
              )}
            </div>
          )}
          {admin && (
            <p className='mt-3 text-xs text-muted-foreground'>
              选中通道后导出所选配置；未选择时导出所有可配置通道。
            </p>
          )}
        </>
      )}
    </>
  )
}
