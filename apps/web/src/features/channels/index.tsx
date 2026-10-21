import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Play, Search, Video } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  collapseUnconfiguredTail,
  fullStamp,
  shortStamp,
  streamSummary,
} from './list-format'
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
import {
  PageTitle,
  QueryState,
  Notices,
  Empty,
  SelectField,
} from '@/features/foundation/ui'
import { cn } from '@/lib/utils'
import { downloadLocal, sourceCommand } from './api'
import { StatusLabel } from './channel-status'
import { reasonLabel } from './status-reasons'
import { MediaParamsCell } from './media-cell'
import { ThumbnailSlot } from './thumbnail-slot'
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

// A channel is "unconfigured" while it has no applied camera source. The
// summary only fills source_ip from the applied revision, so the two agree;
// the explicit state check keeps old cached payloads honest.
function isUnconfigured(channel: Channel) {
  return (
    !channel.source_ip &&
    !channel.current_revision_id &&
    channel.main.state === 'not_configured'
  )
}

type StatusFilter = 'all' | 'normal' | 'disabled' | 'unconfigured' | 'abnormal'

const STATUS_FILTERS: { value: StatusFilter; label: string }[] = [
  { value: 'all', label: '全部' },
  { value: 'normal', label: '🟢 正常' },
  { value: 'disabled', label: '⚪ 已停用' },
  { value: 'unconfigured', label: '⚫ 未配置' },
  { value: 'abnormal', label: '🟠 异常' },
]

// The three observed kinds, typed so `channel[kind]` stays a known key.
const OBSERVED_KINDS = ['main', 'sub', 'recording'] as const

function anyHealthy(channel: Channel) {
  return OBSERVED_KINDS.some((kind) => channel[kind].state === 'healthy')
}

export function Channels({ initialBatch = '' }: { initialBatch?: string }) {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const exportAction = useAction()
  const enableAction = useAction()
  const groupAction = useAction()
  const batchEnableAction = useAction()
  const [showImport, setShowImport] = useState(!!initialBatch)
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<StatusFilter>('all')
  const [group, setGroup] = useState('all')
  const [batchGroup, setBatchGroup] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  // Unconfigured slots at the bottom of the list are filler on a full site;
  // eight of them take a third of the table. Collapsed by default, expandable.
  const [showUnconfigured, setShowUnconfigured] = useState(false)
  const [confirmDisable, setConfirmDisable] = useState<Channel | null>(null)
  const [confirmBatchDisable, setConfirmBatchDisable] = useState(false)
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
  // Clickable status counts on the filter row; they carry the same semantics
  // as the old dropdown's values, so the two never disagree about a row.
  const statusCounts = {
    all: channels.length,
    normal: 0,
    disabled: 0,
    unconfigured: 0,
    abnormal: 0,
  } as Record<StatusFilter, number>
  for (const channel of channels) {
    if (!channel.enabled) statusCounts.disabled++
    else if (isUnconfigured(channel)) statusCounts.unconfigured++
    else if (anyHealthy(channel)) statusCounts.normal++
    else statusCounts.abnormal++
  }
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
    if (filter === 'disabled') return !channel.enabled
    if (filter === 'unconfigured') return isUnconfigured(channel)
    if (filter === 'normal') return channel.enabled && anyHealthy(channel)
    return channel.enabled && !anyHealthy(channel)
  })
  // Only collapse in the "all" view: a filtered view that hides rows the
  // operator explicitly asked for would be a different, surprising behaviour.
  const collapse = filter === 'all' && !showUnconfigured
  const tail = collapse
    ? collapseUnconfiguredTail(visible)
    : { before: visible, hidden: [], rangeLabel: '', collapsed: false }
  const rendered = tail.collapsed ? tail.before : visible
  const permitted = channels.filter((channel) =>
    channel.permissions.includes('configure')
  )
  const chosen = permitted.filter((channel) => selected.has(channel.channel_id))
  const invalidSelection = selected.size > 0 && chosen.length !== selected.size
  // Unconfigured rows stay out of every batch surface: there is nothing to
  // enable (no source to stream) and no source config to export for them.
  const selectable = rendered.filter(
    (channel) =>
      channel.permissions.includes('configure') && !isUnconfigured(channel)
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
  function patchEnabled(list: Channel[], enabled: boolean) {
    return Promise.allSettled(
      list.map((channel) =>
        apiRequest(
          `/api/v1/channels/${channel.channel_id}`,
          jsonRequest('PATCH', { enabled }, channel.version)
        )
      )
    )
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
  // Batch enable/disable walks the same per-row version contract as the
  // single-row toggle: each row carries its own If-Match, so a concurrent
  // edit fails only that row.
  async function batchSetEnabled(enabled: boolean) {
    if (chosen.length === 0) return
    await batchEnableAction.run(async () => {
      const results = await patchEnabled(chosen, enabled)
      const failed = results.filter((result) => result.status === 'rejected')
        .length
      if (failed)
        throw new Error(
          `${chosen.length - failed} 路已更新，${failed} 路失败（配置可能已改变），请刷新后重试`
        )
      setSelected(new Set())
    }, `已把 ${chosen.length} 路设为${enabled ? '启用' : '停用'}；取流与录像约 30 秒内${enabled ? '恢复' : '停止'}`)
  }
  // Each row carries its own expected version, so a concurrent edit fails only
  // that row instead of silently overwriting it.
  async function applyGroup(clear = false) {
    if (chosen.length === 0) return
    const label = clear || !batchGroup ? '（清空分组）' : `「${batchGroup}」`
    await groupAction.run(async () => {
      const results = await Promise.allSettled(
        chosen.map((channel) =>
          apiRequest(
            `/api/v1/channels/${channel.channel_id}`,
            jsonRequest(
              'PATCH',
              { channel_group: clear ? '' : batchGroup },
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
  function exportSelected() {
    void exportAction.run(
      async () => {
        const value = await sourceCommand<Schema<'SourceExportFile'>>(
          '/api/v1/channels/source-config-export',
          {
            channel_ids: chosen.map((channel) => channel.channel_id),
          }
        )
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
      <Notices
        error={batchEnableAction.error}
        notice={batchEnableAction.notice}
      />
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
              configured: channel.current_revision_id != null,
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
              <SelectField
                ariaLabel='分组筛选'
                className='w-40'
                value={group}
                onValueChange={setGroup}
                options={[
                  { value: 'all', label: '全部分组' },
                  { value: 'none', label: '未分组' },
                  ...groups.map((value) => ({ value, label: value })),
                ]}
              />
            </div>
            <div
              role='group'
              aria-label='状态筛选'
              className='flex flex-wrap items-center gap-1'
            >
              {STATUS_FILTERS.map(({ value, label }) => {
                const active = filter === value
                return (
                  <button
                    key={value}
                    type='button'
                    aria-pressed={active}
                    onClick={() => setFilter(value)}
                    className={`inline-flex h-7 items-center gap-1 rounded-full border px-2.5 text-xs transition-colors ${
                      active
                        ? 'border-primary bg-primary/10 font-medium text-primary'
                        : 'border-border text-muted-foreground hover:bg-accent hover:text-foreground'
                    }`}
                  >
                    {label}
                    <span
                      className={`tabular-nums ${active ? '' : 'opacity-70'}`}
                    >
                      {statusCounts[value]}
                    </span>
                  </button>
                )
              })}
            </div>
          </div>
          {selected.size > 0 && (
            <div className='mb-3 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-primary/30 bg-primary/5 px-3.5 py-2 shadow-2xs'>
              <div className='flex flex-wrap items-center gap-1.5'>
                <span className='text-sm font-medium whitespace-nowrap'>
                  已选 {chosen.length} 个通道
                </span>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  disabled={
                    batchEnableAction.pending || enableAction.pending
                  }
                  onClick={() => void batchSetEnabled(true)}
                >
                  批量启用
                </Button>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  disabled={
                    batchEnableAction.pending || enableAction.pending
                  }
                  onClick={() => setConfirmBatchDisable(true)}
                >
                  批量停用
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  disabled={exportAction.pending}
                  onClick={exportSelected}
                >
                  导出所选配置
                </Button>
                {invalidSelection && (
                  <span role='alert' className='text-xs'>
                    部分所选通道已不可配置，请取消后重新选择。
                  </span>
                )}
              </div>
              <div className='flex flex-wrap items-center gap-2'>
                <label className='flex items-center gap-1.5 text-sm whitespace-nowrap text-muted-foreground'>
                  修改分组:
                  <input
                    aria-label='批量分组'
                    className='h-7 w-36 rounded-md border border-border bg-background px-2 text-xs'
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
                  size='sm'
                  disabled={groupAction.pending || chosen.length === 0}
                  onClick={() => void applyGroup(false)}
                >
                  应用分组
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  disabled={groupAction.pending || chosen.length === 0}
                  onClick={() => void applyGroup(true)}
                >
                  清空分组
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  onClick={() => setSelected(new Set())}
                >
                  取消选择
                </Button>
              </div>
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
                        <Checkbox
                          aria-label='选择当前结果'
                          className='rounded border-zinc-600'
                          checked={allChecked}
                          disabled={!selectable.length}
                          onCheckedChange={(state) => {
                            const checked = state === true
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
                    <TableHead className='min-w-36'>媒体参数</TableHead>
                    <TableHead className='w-20 pr-6 text-right'>码率</TableHead>
                    <TableHead className='w-24'>最近错误</TableHead>
                    <TableHead className='w-32'>更新时间</TableHead>
                    <TableHead className='w-40 text-right'>操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rendered.map((channel) => {
                    const unconfigured = isUnconfigured(channel)
                    const normal = anyHealthy(channel)
                    return (
                      <TableRow
                        key={channel.channel_id}
                        data-testid='channel-row'
                        className={cn(
                          normal &&
                            'bg-emerald-950/15 hover:bg-emerald-950/25'
                        )}
                      >
                        {admin && (
                          <TableCell>
                            <Checkbox
                              aria-label={`选择 ${numberLabel(channel)}`}
                              className='rounded border-zinc-600'
                              checked={chosen.some(
                                (item) =>
                                  item.channel_id === channel.channel_id
                              )}
                              disabled={
                                unconfigured ||
                                !channel.permissions.includes('configure')
                              }
                              title={
                                unconfigured
                                  ? '未配置摄像头，先完成接入再选择'
                                  : undefined
                              }
                              onCheckedChange={(state) =>
                                select(channel.channel_id, state === true)
                              }
                            />
                          </TableCell>
                        )}
                        <TableCell className='font-mono text-xs'>
                          {numberLabel(channel)}
                        </TableCell>
                        <TableCell>
                          <div className='flex items-center gap-3'>
                            <ThumbnailSlot
                              online={normal}
                              disabled={!channel.enabled}
                              unconfigured={unconfigured}
                              label={`${numberLabel(channel)} ${channel.channel_name}`}
                            />
                            <span
                              className={cn(
                                'font-medium',
                                unconfigured && 'text-zinc-500'
                              )}
                            >
                              {channel.channel_name}
                            </span>
                            {!channel.enabled && (
                              <span className='rounded-md bg-muted px-1.5 py-0.5 text-xs text-muted-foreground'>
                                已停用
                              </span>
                            )}
                            {unconfigured && (
                              <span className='rounded-md border border-border bg-zinc-100 px-1.5 py-0.5 text-xs text-zinc-500 dark:bg-zinc-800/80 dark:text-zinc-400'>
                                未配置
                              </span>
                            )}
                          </div>
                        </TableCell>
                        <TableCell className='text-sm text-muted-foreground'>
                          {channel.channel_group || '—'}
                        </TableCell>
                        <TableCell
                          className='text-xs text-muted-foreground'
                          title={
                            channel.source_ip
                              ? streamSummary(channel)
                              : undefined
                          }
                        >
                          {channel.source_ip ? (
                            <span className='inline-flex items-center gap-1.5'>
                              {channel.source_ip}
                              {channel.current_revision_id != null &&
                                (channel.onvif_port != null ? (
                                  <span className='rounded border border-border px-1 py-px text-[10px] leading-4 text-muted-foreground'>
                                    ONVIF
                                  </span>
                                ) : (
                                  <span className='rounded border border-border px-1 py-px text-[10px] leading-4 text-muted-foreground'>
                                    RTSP
                                  </span>
                                ))}
                            </span>
                          ) : (
                            <span className={unconfigured ? 'text-zinc-600' : 'text-zinc-500'}>
                              未配置摄像头
                            </span>
                          )}
                        </TableCell>
                        {(['main', 'sub', 'recording'] as const).map((kind) => (
                          <TableCell key={kind} className='text-center'>
                            {unconfigured ? (
                              <span className='text-xs text-zinc-600'>—</span>
                            ) : (
                              <StatusLabel value={channel[kind]} />
                            )}
                          </TableCell>
                        ))}
                        <TableCell className='text-xs'>
                          {unconfigured ? (
                            <span className='text-zinc-600'>—</span>
                          ) : (
                            <MediaParamsCell channel={channel} zone={zone} />
                          )}
                        </TableCell>
                        {/* The rate is right-aligned and the error text is
                            left-aligned; without the gap "244 kbps" and "无"
                            read as one string. */}
                        <TableCell className='pr-6 text-right text-sm tabular-nums'>
                          {unconfigured ? (
                            <span className='text-zinc-600'>—</span>
                          ) : (
                            bitrateLabel(channel.bitrate_kbps)
                          )}
                        </TableCell>
                        <TableCell className='text-sm'>
                          {/* An unconfigured channel has never been observed, so
                              "无" (no errors) would be a claim about a camera
                              that does not exist yet; it reads "—" like the rest
                              of its empty cells. */}
                          {unconfigured ? (
                            <span className='text-zinc-600'>—</span>
                          ) : channel.last_error ? (
                            reasonLabel(channel.last_error)
                          ) : (
                            <span className='text-muted-foreground'>无</span>
                          )}
                        </TableCell>
                        <TableCell className='text-xs text-muted-foreground'>
                          {channel.updated_at && zone ? (
                            <time
                              dateTime={channel.updated_at}
                              title={fullStamp(
                                Date.parse(channel.updated_at),
                                zone
                              )}
                            >
                              {shortStamp(Date.parse(channel.updated_at), zone)}
                            </time>
                          ) : (
                            '—'
                          )}
                        </TableCell>
                        <TableCell className='text-right'>
                          <div className='flex items-center justify-end gap-1'>
                            {normal &&
                              channel.permissions.includes('live') && (
                                <Link
                                  to='/live'
                                  search={{ channel: channel.channel_id }}
                                  aria-label={`预览 ${numberLabel(channel)}`}
                                  title={`预览 ${numberLabel(channel)}`}
                                  className='inline-flex h-7 items-center gap-1 rounded-md border px-2 text-xs hover:bg-accent'
                                >
                                  <Play className='size-3' />
                                  预览
                                </Link>
                              )}
                            {/* Disabling a channel stops streaming and
                                recording but keeps history playable, so the
                                recording shortcut belongs to every configured
                                channel, not just the online ones. */}
                            {!unconfigured &&
                              channel.permissions.includes('playback') && (
                                <Link
                                  to='/recordings'
                                  search={{
                                    channel: channel.channel_id,
                                    at: '',
                                    tab: 'playback',
                                  }}
                                  aria-label={`录像 ${numberLabel(channel)}`}
                                  title={`查看 ${numberLabel(channel)} 录像`}
                                  className='inline-flex h-7 items-center gap-1 rounded-md border px-2 text-xs hover:bg-accent'
                                >
                                  <Video className='size-3' />
                                  录像
                                </Link>
                              )}
                            <Link
                              to='/channels/configure'
                              search={{ channel: channel.channel_id, tab: '' }}
                              aria-label={`配置 ${numberLabel(channel)}`}
                              className='inline-flex h-7 items-center rounded-md border px-2 text-xs font-medium hover:bg-accent'
                            >
                              配置
                            </Link>
                            {admin &&
                              !unconfigured &&
                              channel.permissions.includes('configure') &&
                              (channel.enabled ? (
                                <Button
                                  type='button'
                                  variant='ghost'
                                  size='sm'
                                  className='h-7 px-2 text-xs'
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
                                  className='h-7 px-2 text-xs'
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
                    )
                  })}
                  {tail.collapsed && tail.hidden.length > 0 && (
                    <TableRow>
                      <TableCell
                        colSpan={admin ? 13 : 12}
                        className='bg-muted/30 text-xs text-muted-foreground'
                      >
                        <button
                          type='button'
                          className='cursor-pointer underline-offset-2 hover:underline'
                          aria-expanded={showUnconfigured}
                          onClick={() => setShowUnconfigured(true)}
                        >
                          已折叠 {tail.hidden.length} 个未配置通道（
                          {tail.rangeLabel}）· 点击展开
                        </button>
                      </TableCell>
                    </TableRow>
                  )}
                  {!tail.collapsed && tail.hidden.length > 0 && (
                    <TableRow>
                      <TableCell
                        colSpan={admin ? 13 : 12}
                        className='bg-muted/30 text-xs text-muted-foreground'
                      >
                        <button
                          type='button'
                          className='cursor-pointer underline-offset-2 hover:underline'
                          aria-expanded={showUnconfigured}
                          onClick={() => setShowUnconfigured(false)}
                        >
                          收起 {tail.hidden.length} 个未配置通道（
                          {tail.rangeLabel}）
                        </button>
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
              {visible.length === 0 && (
                <div className='p-8 text-center text-sm text-muted-foreground'>
                  没有匹配的通道
                </div>
              )}
            </div>
          )}
          {/* The hint is only for the nothing-selected state: once a row is
              ticked, the batch bar above already says how many and what can be
              done, and repeating it here made the count appear twice. */}
          {admin && selected.size === 0 && (
            <p className='mt-3 text-xs text-muted-foreground/70'>
              勾选通道后可批量启停、改分组或导出配置；未配置摄像头的通道不参与批量操作。
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
      <ConfirmDialog
        open={confirmBatchDisable}
        onOpenChange={setConfirmBatchDisable}
        title={`停用选中的 ${chosen.length} 路？`}
        desc='停用后这些通道停止取流与录像，录像策略、摄像头配置与历史录像都保留。约 30 秒内生效。'
        confirmText='批量停用'
        cancelBtnText='取消'
        destructive
        isLoading={batchEnableAction.pending}
        handleConfirm={() => {
          setConfirmBatchDisable(false)
          void batchSetEnabled(false)
        }}
      />
    </>
  )
}
