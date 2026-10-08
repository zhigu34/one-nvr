import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Search } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ConfirmDialog } from '@/components/confirm-dialog'
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
import { formatMinute } from '@/features/playback/zone'
import { downloadLocal, sourceCommand } from './api'
import { StatusLabel } from './channel-status'
import { reasonLabel } from './status-reasons'
import { SourceImportControls } from './source-import-controls'

type Channel = Schema<'ChannelSummary'>

function numberLabel(channel: { channel_no: number }) {
  return `CH${String(channel.channel_no).padStart(2, '0')}`
}

// Rounding only: a rate is never invented, only shortened for display.
function bitrateLabel(kbps: number | null | undefined) {
  if (kbps == null) return '—'
  return kbps >= 1000
    ? `${(kbps / 1000).toFixed(1)} Mbps`
    : `${Math.round(kbps)} kbps`
}

export function Channels({ initialBatch = '' }: { initialBatch?: string }) {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const exportAction = useAction()
  const enableAction = useAction()
  const groupAction = useAction()
  const [showImport, setShowImport] = useState(!!initialBatch)
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState('all')
  const [group, setGroup] = useState('all')
  const [batchGroup, setBatchGroup] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [confirmDisable, setConfirmDisable] = useState<Channel | null>(null)
  // One request for the whole page. The previous shape polled one status
  // request per channel every five seconds, which is 32 requests on a full
  // site just to draw this table.
  const summary = useAPI<Schema<'ChannelSummaryPage'>>(
    '/api/v1/channels/summary',
    true,
    5000
  )
  const site = useAPI<Schema<'Site'>>('/api/v1/site', true, false)
  const zone = site.data?.timezone || ''
  const channels = summary.data?.items || []
  const groups = [
    ...new Set(channels.map((channel) => channel.channel_group).filter(Boolean)),
  ].sort()
  const visible = channels.filter((channel) => {
    const match =
      `${numberLabel(channel)} ${channel.channel_no} ${channel.channel_name} ${channel.channel_group}`
        .toLowerCase()
        .includes(search.toLowerCase().trim())
    if (!match) return false
    if (group === 'none' && channel.channel_group) return false
    if (group !== 'all' && group !== 'none' && channel.channel_group !== group)
      return false
    if (filter === 'all') return true
    if (filter === 'configured') return !!channel.current_revision_id
    if (filter === 'empty') return !channel.current_revision_id
    return (['main', 'sub', 'recording'] as const).some((kind) =>
      ['unavailable', 'degraded'].includes(channel[kind].state)
    )
  })
  const permitted = channels.filter((channel) =>
    channel.permissions.includes('configure')
  )
  const chosen = permitted.filter((channel) => selected.has(channel.channel_id))
  const invalidSelection = selected.size > 0 && chosen.length !== selected.size
  const selectable = visible.filter((channel) =>
    channel.permissions.includes('configure')
  )
  const allChecked =
    selectable.length > 0 &&
    selectable.every((channel) => selected.has(channel.channel_id))
  function select(id: string, checked: boolean) {
    setSelected((previous) => {
      const next = new Set(previous)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }
  async function setEnabled(channel: Channel, enabled: boolean) {
    await enableAction.run(
      () =>
        apiRequest(
          `/api/v1/channels/${channel.channel_id}`,
          jsonRequest('PATCH', { enabled }, channel.version)
        ),
      enabled
        ? '通道已启用；取流与录像会在下一次调度时恢复'
        : '通道已停用；取流与录像会在约 30 秒内停止，历史录像保留'
    )
  }
  // Each row carries its own expected version, so a concurrent edit fails only
  // that row instead of silently overwriting it.
  async function applyGroup() {
    if (chosen.length === 0) return
    const label = batchGroup ? `「${batchGroup}」` : '（清空分组）'
    await groupAction.run(async () => {
      const results = await Promise.allSettled(
        chosen.map((channel) =>
          apiRequest(
            `/api/v1/channels/${channel.channel_id}`,
            jsonRequest(
              'PATCH',
              { channel_group: batchGroup },
              channel.version
            )
          )
        )
      )
      const failed = results.filter(
        (result) => result.status === 'rejected'
      ).length
      if (failed)
        throw new Error(
          `${chosen.length - failed} 路已更新，${failed} 路失败（配置可能已改变），请刷新后重试`
        )
      setSelected(new Set())
    }, `已把 ${chosen.length} 路设为 ${label}`)
  }
  return (
    <>
      <div className='mb-5 flex flex-wrap items-start justify-between gap-4'>
        <PageTitle
          title='通道管理'
          description='集中查看连接与录像状态，选择通道进行配置。'
        />
        {admin && summary.data && (
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
                        (channel) => channel.channel_id
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
      <Notices error={exportAction.error} notice={exportAction.notice} />
      <Notices error={enableAction.error} notice={enableAction.notice} />
      <Notices error={groupAction.error} notice={groupAction.notice} />
      <QueryState
        pending={summary.isPending}
        error={summary.error}
        retry={() => summary.refetch()}
      />
      {admin && summary.data && showImport && (
        <div className='mb-5 rounded-xl border bg-card p-5'>
          <SourceImportControls
            key={initialBatch}
            channels={channels.map((channel) => ({
              id: channel.channel_id,
              channel_no: channel.channel_no,
              channel_name: channel.channel_name,
              channel_group: channel.channel_group,
              enabled: channel.enabled,
              version: channel.version,
              permissions: channel.permissions,
            }))}
            initialBatch={initialBatch}
          />
        </div>
      )}
      {summary.data && (
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
                  placeholder='搜索通道编号、名称或分组'
                  className='pl-9'
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                />
              </div>
              <select
                aria-label='分组筛选'
                className='h-9 rounded-md border bg-background px-3 text-sm'
                value={group}
                onChange={(event) => setGroup(event.target.value)}
              >
                <option value='all'>全部分组</option>
                <option value='none'>未分组</option>
                {groups.map((value) => (
                  <option key={value} value={value}>
                    {value}
                  </option>
                ))}
              </select>
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
            <div className='mb-3 flex flex-wrap items-end gap-3 rounded-xl border bg-card p-4'>
              <label className='grid gap-2 text-sm'>
                批量分组
                <input
                  aria-label='批量分组'
                  className='h-9 rounded-md border bg-background px-3 text-sm'
                  list='existing-channel-groups'
                  placeholder='留空则清空分组'
                  maxLength={64}
                  value={batchGroup}
                  onChange={(event) => setBatchGroup(event.target.value)}
                />
              </label>
              <datalist id='existing-channel-groups'>
                {groups.map((value) => (
                  <option key={value} value={value} />
                ))}
              </datalist>
              <Button
                type='button'
                variant='outline'
                disabled={groupAction.pending || chosen.length === 0}
                onClick={() => void applyGroup()}
              >
                应用到选中 {chosen.length} 路
              </Button>
              <Button
                variant='ghost'
                onClick={() => setSelected(new Set())}
              >
                取消选择
              </Button>
              {invalidSelection && (
                <span role='alert' className='pb-2 text-sm'>
                  部分所选通道已不可配置，请取消后重新选择。
                </span>
              )}
            </div>
          )}
          {channels.length === 0 ? (
            <Empty>没有已授权的通道。请联系管理员分配权限。</Empty>
          ) : (
            <div className='overflow-x-auto rounded-xl border bg-card'>
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
                              selectable.forEach((channel) => {
                                if (checked) next.add(channel.channel_id)
                                else next.delete(channel.channel_id)
                              })
                              return next
                            })
                          }}
                        />
                      </TableHead>
                    )}
                    <TableHead>通道</TableHead>
                    <TableHead>名称</TableHead>
                    <TableHead>分组</TableHead>
                    <TableHead>摄像头</TableHead>
                    <TableHead className='w-20 text-center'>主流</TableHead>
                    <TableHead className='w-20 text-center'>子流</TableHead>
                    <TableHead className='w-20 text-center'>录像</TableHead>
                    <TableHead className='w-24 text-right'>码率</TableHead>
                    <TableHead className='min-w-40'>最近错误</TableHead>
                    <TableHead className='w-36'>更新时间</TableHead>
                    <TableHead className='w-32 text-right'>操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {visible.map((channel) => (
                    <TableRow key={channel.channel_id} data-testid='channel-row'>
                      {admin && (
                        <TableCell>
                          <input
                            type='checkbox'
                            aria-label={`选择 ${numberLabel(channel)}`}
                            checked={chosen.some(
                              (item) => item.channel_id === channel.channel_id
                            )}
                            disabled={
                              !channel.permissions.includes('configure')
                            }
                            onChange={(event) =>
                              select(channel.channel_id, event.target.checked)
                            }
                          />
                        </TableCell>
                      )}
                      <TableCell className='font-mono text-xs'>
                        {numberLabel(channel)}
                      </TableCell>
                      <TableCell className='font-medium'>
                        {channel.channel_name}
                        {!channel.enabled && (
                          <span className='ml-2 rounded-md bg-muted px-1.5 py-0.5 text-xs text-muted-foreground'>
                            已停用
                          </span>
                        )}
                      </TableCell>
                      <TableCell className='text-sm text-muted-foreground'>
                        {channel.channel_group || '—'}
                      </TableCell>
                      <TableCell
                        className='text-xs text-muted-foreground'
                        title={
                          channel.source_ip
                            ? `${channel.source_ip}${channel.main_path}`
                            : undefined
                        }
                      >
                        {channel.source_ip
                          ? `${channel.source_ip} · ${channel.main_path}`
                          : '未配置摄像头'}
                      </TableCell>
                      {(['main', 'sub', 'recording'] as const).map((kind) => (
                        <TableCell key={kind} className='text-center'>
                          <StatusLabel value={channel[kind]} />
                        </TableCell>
                      ))}
                      <TableCell className='text-right text-sm tabular-nums'>
                        {bitrateLabel(channel.bitrate_kbps)}
                      </TableCell>
                      <TableCell className='text-sm'>
                        {channel.last_error ? (
                          reasonLabel(channel.last_error)
                        ) : (
                          <span className='text-muted-foreground'>无</span>
                        )}
                      </TableCell>
                      <TableCell className='text-xs text-muted-foreground'>
                        {channel.updated_at && zone
                          ? formatMinute(Date.parse(channel.updated_at), zone)
                          : '—'}
                      </TableCell>
                      <TableCell className='text-right'>
                        <div className='flex items-center justify-end gap-2'>
                          {channel.permissions.includes('configure') ? (
                            <Link
                              to='/channels/configure'
                              search={{ channel: channel.channel_id }}
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
                          {admin &&
                            channel.permissions.includes('configure') &&
                            (channel.enabled ? (
                              <Button
                                type='button'
                                variant='ghost'
                                size='sm'
                                disabled={enableAction.pending}
                                aria-label={`停用 ${numberLabel(channel)}`}
                                onClick={() => setConfirmDisable(channel)}
                              >
                                停用
                              </Button>
                            ) : (
                              <Button
                                type='button'
                                variant='ghost'
                                size='sm'
                                disabled={enableAction.pending}
                                aria-label={`启用 ${numberLabel(channel)}`}
                                onClick={() => void setEnabled(channel, true)}
                              >
                                启用
                              </Button>
                            ))}
                        </div>
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
              选中通道后导出所选配置；未选择时导出所有可配置通道。停用只停止取流与录像，不影响历史回放。
            </p>
          )}
        </>
      )}
      <ConfirmDialog
        open={!!confirmDisable}
        onOpenChange={(open) => {
          if (!open) setConfirmDisable(null)
        }}
        title={`停用 ${confirmDisable ? numberLabel(confirmDisable) : ''}？`}
        desc='停用后该通道停止取流与录像，录像策略、摄像头配置与历史录像都保留。约 30 秒内生效。'
        confirmText='停用'
        cancelBtnText='取消'
        destructive
        isLoading={enableAction.pending}
        handleConfirm={() => {
          const target = confirmDisable
          setConfirmDisable(null)
          if (target) void setEnabled(target, false)
        }}
      />
    </>
  )
}
