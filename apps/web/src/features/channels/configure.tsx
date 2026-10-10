import { useEffect, useMemo, useState } from 'react'
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { useAPI, useAction, fields, text } from '@/features/foundation/hooks'
import {
  PageTitle,
  Forbidden,
  QueryState,
  Notices,
  Field,
  SelectField,
} from '@/features/foundation/ui'
import { sourceCommand } from './api'
import { ChannelStatus } from './channel-status'
import { dotTones, statusTone } from './status-labels'
import { connectSource } from './source-connect'
import { CredentialReveal } from './source-credentials'
import { SourceForm } from './source-form'
import { SourceHistory } from './source-history'
import { SourceTestControls } from './source-test'

function numberLabel(channel: { channel_no: number }) {
  return `CH${String(channel.channel_no).padStart(2, '0')}`
}

const MODE_LABELS: Record<string, string> = {
  none: '关闭录像',
  continuous: '手动（连续录像）',
}

// The picker answers "which channel am I editing" from the same summary
// request the list page uses: grouping, search and the state dots all come
// from data already loaded, not from one status request per channel.
function ChannelPicker({
  items,
  selectedId,
  onSelect,
}: {
  items: Schema<'ChannelSummary'>[]
  selectedId: string
  onSelect: (value: string) => void
}) {
  const [search, setSearch] = useState('')
  const query = search.toLowerCase().trim()
  const visible = items.filter((channel) =>
    `${numberLabel(channel)} ${channel.channel_no} ${channel.channel_name} ${channel.channel_group}`
      .toLowerCase()
      .includes(query)
  )
  const groups = useMemo(() => {
    const order: string[] = []
    const byName = new Map<string, Schema<'ChannelSummary'>[]>()
    for (const channel of visible) {
      const name = channel.channel_group || '未分组'
      if (!byName.has(name)) {
        byName.set(name, [])
        order.push(name)
      }
      byName.get(name)!.push(channel)
    }
    return order.map((name) => ({ name, items: byName.get(name)! }))
  }, [visible])
  return (
    <aside className='rounded-xl border bg-card p-3 lg:sticky lg:top-20'>
      <a
        href='/channels'
        className='mb-3 block px-1 text-sm text-muted-foreground hover:text-foreground'
      >
        ← 返回通道列表
      </a>
      <SelectField
        label='选择通道'
        className='text-sm'
        value={selectedId}
        onValueChange={onSelect}
        options={items.map((value) => ({
          value: value.channel_id,
          label: `${numberLabel(value)} · ${value.channel_name}`,
        }))}
      />
      <div className='relative mt-3'>
        <Input
          aria-label='搜索通道'
          className='h-8 pr-2 pl-8 text-sm'
          placeholder='搜索通道 / 名称'
          value={search}
          onChange={(event) => setSearch(event.target.value)}
        />
        <svg
          aria-hidden
          className='pointer-events-none absolute top-2 left-2.5 size-4 text-muted-foreground'
          viewBox='0 0 24 24'
          fill='none'
          stroke='currentColor'
          strokeWidth='2'
        >
          <circle cx='11' cy='11' r='7' />
          <path d='m20 20-3.5-3.5' />
        </svg>
      </div>
      <nav
        aria-label='切换通道'
        className='mt-2 hidden max-h-[calc(100vh-16rem)] space-y-2 overflow-y-auto pr-0.5 lg:block'
      >
        {groups.map((group) => (
          <details
            key={group.name}
            // The group holding the edited channel always opens, and short
            // groups start open too, so a 32-channel site is not a wall of
            // collapsed headings.
            open={
              group.items.some(
                (item) => item.channel_id === selectedId
              ) || group.items.length <= 8
            }
          >
            <summary className='flex cursor-pointer items-center justify-between gap-2 rounded px-2 py-1 text-[11px] tracking-wide text-muted-foreground hover:text-foreground'>
              <span className='truncate'>{group.name}</span>
              <span className='font-mono'>{group.items.length}</span>
            </summary>
            <div className='mt-1 space-y-px'>
              {group.items.map((item) => {
                const chosen = item.channel_id === selectedId
                return (
                  <button
                    key={item.channel_id}
                    type='button'
                    aria-current={chosen ? 'true' : undefined}
                    onClick={() => onSelect(item.channel_id)}
                    className={`relative flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-[13px] ${
                      chosen ? 'bg-accent font-medium' : 'hover:bg-accent'
                    }`}
                  >
                    {chosen && (
                      <span
                        aria-hidden
                        className='absolute inset-y-1 left-0 w-0.5 rounded-full bg-foreground'
                      />
                    )}
                    <span className='font-mono text-[11px] text-muted-foreground'>
                      {numberLabel(item)}
                    </span>
                    <span
                      className={`flex-1 truncate ${
                        item.enabled ? '' : 'text-muted-foreground line-through'
                      }`}
                    >
                      {item.channel_name}
                    </span>
                    <span
                      aria-hidden
                      title={
                        item.enabled ? undefined : '通道已停用，不取流也不录像'
                      }
                      className={`size-1.5 shrink-0 rounded-full ${
                        item.enabled
                          ? dotTones[statusTone(item.main)]
                          : dotTones.muted
                      }`}
                    />
                  </button>
                )
              })}
            </div>
          </details>
        ))}
        {groups.length === 0 && (
          <p className='px-2 py-3 text-xs text-muted-foreground'>
            没有匹配的通道
          </p>
        )}
      </nav>
    </aside>
  )
}

export function ChannelConfigure({ initialId = '' }: { initialId?: string }) {
  // One request for the whole page. The previous shape polled one status
  // request per channel and could not show group or enable state at all.
  const summary = useAPI<Schema<'ChannelSummaryPage'>>(
    '/api/v1/channels/summary',
    true,
    5000
  )
  const [selected, setSelected] = useState(initialId)
  const allowed = (summary.data?.items || []).filter((channel) =>
    channel.permissions.includes('configure')
  )
  const channel = allowed.find(
    (value) => value.channel_id === (selected || allowed[0]?.channel_id)
  )
  return (
    <>
      <PageTitle title='通道配置' description='选择通道，设置摄像头连接。' />
      <QueryState
        pending={summary.isPending}
        error={summary.error}
        retry={() => summary.refetch()}
      />
      {!summary.isPending && !summary.error && allowed.length === 0 && (
        <Forbidden />
      )}
      {allowed.length > 0 && (
        <div className='grid items-start gap-5 lg:grid-cols-[248px_minmax(0,1fr)]'>
          <ChannelPicker
            items={allowed}
            selectedId={channel?.channel_id || ''}
            onSelect={setSelected}
          />
          <div className='min-w-0'>
            {channel && (
              <ConfigureChannel
                key={channel.channel_id}
                channel={channel}
                onConfigured={setSelected}
              />
            )}
          </div>
        </div>
      )}
    </>
  )
}

function ConfigureChannel({
  channel,
  onConfigured,
}: {
  channel: Schema<'ChannelSummary'>
  onConfigured: (value: string) => void
}) {
  const client = useQueryClient()
  const admin = useAuthStore((s) => s.user?.role === 'admin')
  const id = channel.channel_id
  const status = useAPI<Schema<'ChannelSourceStatus'>>(
    `/api/v1/channels/${id}/source/status`,
    true,
    2000
  )
  // The recording mode is only displayed here; the recording plan page owns
  // changing it. A read failure hides the line rather than blocking the page.
  const policy = useAPI<Schema<'RecordingPolicy'>>(
    `/api/v1/channels/${id}/recording-policy`,
    true,
    10000
  )
  const history = useInfiniteQuery({
    queryKey: [`/api/v1/channels/${id}/source-revisions`],
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      apiRequest<Schema<'RevisionPage'>>(
        `/api/v1/channels/${id}/source-revisions?limit=100${pageParam ? '&cursor=' + pageParam : ''}`,
        { signal }
      ),
    getNextPageParam: (page) => page.next_cursor || undefined,
    refetchInterval: 5000,
    retry: false,
  })
  const revisions = history.data?.pages.flatMap((page) => page.items) || []
  const [selected, setSelected] = useState<Schema<'SourceRevision'> | null>(
    null
  )
  const [connecting, setConnecting] = useState(false)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [confirmClear, setConfirmClear] = useState(false)
  const [testId, setTestId] = useState(''),
    [jobId, setJobId] = useState(''),
    [notice, setNotice] = useState(''),
    [error, setError] = useState(''),
    [pending, setPending] = useState(false)
  const proof = useAPI<Schema<'SourceTestResult'>>(
    `/api/v1/channels/${id}/source-tests/${testId}`,
    !!testId,
    1000
  )
  const job = useAPI<Schema<'Job'>>(`/api/v1/jobs/${jobId}`, !!jobId, 1000)
  useEffect(() => {
    if (job.data?.state === 'succeeded') {
      void client.invalidateQueries()
    }
  }, [job.data?.state, job.data?.error_code, client])
  const current =
    revisions.find((item) => item.id === status.data?.current_revision_id) ||
    null
  const activeRevision = selected || current
  function accepted(value: Schema<'SourceChange'>, kind?: 'test' | 'apply') {
    if (kind === 'test') {
      setJobId('')
      setTestId(value.test_id || '')
      setNotice('测试已排队')
    } else {
      setJobId(value.job_id)
      setNotice('操作已排队，等待执行结果')
    }
    setError('')
    void client.invalidateQueries()
  }
  // Configuration permissions are rechecked on every poll. Unmount secret-bearing controls on failure.
  if (history.error || status.error)
    return (
      <QueryState
        pending={false}
        error={history.error || status.error}
        retry={() => client.invalidateQueries()}
      />
    )
  if (!status.data || !history.data) return <QueryState pending error={null} />
  // Names, grouping and the source form all travel on the channel's own
  // version, so the newest value wins over a stale list row.
  const version = Math.max(channel.version, status.data.version)
  const configured = channel.current_revision_id != null
  return (
    <>
      <Notices
        notice={job.data?.state === 'succeeded' ? '操作已完成' : notice}
        error={
          job.data?.state === 'failed'
            ? `执行失败：${job.data.error_code || '请检查通道状态'}`
            : error
        }
      />
      {job.data && (
        <p className='mb-4 text-sm'>
          后台任务：
          {
            {
              queued: '排队中',
              running: '执行中',
              succeeded: '已完成',
              failed: '失败',
            }[job.data.state]
          }
        </p>
      )}
      <Card className='mb-5 gap-0 py-0'>
        <div className='flex flex-wrap items-start justify-between gap-4 p-5'>
          <div className='min-w-0'>
            <div className='flex flex-wrap items-center gap-2'>
              <span className='font-mono text-xs text-muted-foreground'>
                {numberLabel(channel)}
              </span>
              {channel.channel_group && (
                <span className='rounded border px-1.5 py-0.5 text-[11px] text-muted-foreground'>
                  {channel.channel_group}
                </span>
              )}
              {configured && (
                <span className='rounded border px-1.5 py-0.5 text-[11px] text-muted-foreground'>
                  {channel.onvif_port != null ? 'ONVIF 手动' : 'RTSP 手动'}
                </span>
              )}
            </div>
            <h2 className='mt-1.5 flex items-center gap-2 text-lg font-semibold'>
              {channel.channel_name}
              <Button
                type='button'
                variant='outline'
                size='sm'
                className='h-6 px-1.5 text-[11px] font-normal text-muted-foreground'
                aria-expanded={editing}
                title='修改通道名称与分组，不会重建媒体流'
                onClick={() => setEditing(!editing)}
              >
                编辑
              </Button>
            </h2>
            <p
              className='mt-1 font-mono text-xs text-muted-foreground'
              title={
                channel.source_ip && channel.main_path
                  ? `${channel.source_ip}${channel.main_path}`
                  : undefined
              }
            >
              {channel.source_ip
                ? `${channel.source_ip} · ${channel.main_path || '未填写主流路径'}`
                : '未配置摄像头'}
            </p>
          </div>
          <div className='flex flex-col items-end gap-2'>
            <ChannelStatus value={status.data} />
            <div className='flex items-center gap-3'>
              <span className='inline-flex items-center gap-1.5 text-xs'>
                <span
                  aria-hidden
                  className={`size-1.5 rounded-full ${
                    channel.enabled ? dotTones.ok : dotTones.muted
                  }`}
                />
                {channel.enabled ? '通道启用中' : '通道已停用'}
              </span>
              <ChannelEnableToggle
                channel={channel}
                version={version}
              />
            </div>
          </div>
        </div>
        {editing && (
          <div className='border-t px-5 py-4'>
            <ChannelBasics
              channel={{ ...channel, version }}
              done={() => setEditing(false)}
            />
          </div>
        )}
        <div className='flex flex-wrap items-center justify-between gap-3 border-t px-5 py-3'>
          <p className='text-xs text-muted-foreground'>
            录像方式：
            <span className='text-foreground'>
              {policy.data
                ? MODE_LABELS[policy.data.mode] || policy.data.mode
                : '—'}
            </span>
            <span className='mx-1'>·</span>
            <a href='/recording-plan' className='underline underline-offset-2'>
              在录像计划页修改
            </a>
          </p>
          <details className='text-xs'>
            <summary className='cursor-pointer text-muted-foreground'>
              危险操作
            </summary>
            <div className='mt-2 flex flex-wrap items-center gap-2'>
              <Button
                type='button'
                variant='outline'
                size='sm'
                className='border-destructive/40 text-destructive'
                disabled={pending || !configured}
                onClick={() => setConfirmClear(true)}
              >
                清空摄像头配置
              </Button>
              {!configured && (
                <span className='text-[11px] text-muted-foreground'>
                  通道尚未接入摄像头
                </span>
              )}
            </div>
          </details>
        </div>
      </Card>
      <ConfirmDialog
        open={confirmClear}
        onOpenChange={setConfirmClear}
        title='清空此通道的摄像头？'
        desc='清空会停止当前取流和录像，通道编号、权限及历史录像保留。'
        confirmText='清空摄像头'
        cancelBtnText='取消'
        destructive
        isLoading={pending}
        handleConfirm={async () => {
          setConfirmClear(false)
          setPending(true)
          setError('')
          try {
            const value = await sourceCommand<Schema<'SourceChange'>>(
              `/api/v1/channels/${id}/source/clear`,
              {},
              version
            )
            accepted(value)
            setSelected(null)
            setTestId('')
          } catch (e) {
            setError(e instanceof Error ? e.message : '清空失败')
          } finally {
            setPending(false)
          }
        }}
      />
      <div className='mb-3 flex flex-wrap items-center justify-between gap-3 px-1'>
        <h3 className='text-sm font-medium'>摄像头连接</h3>
        <Button
          type='button'
          variant='outline'
          size='sm'
          aria-expanded={historyOpen}
          onClick={() => setHistoryOpen(!historyOpen)}
        >
          历史与诊断
        </Button>
      </div>
      {historyOpen && (
        <Card className='mb-5 gap-0 py-0'>
          <div className='space-y-5 p-5'>
            <section>
              <h4 className='mb-3 text-sm font-medium'>历史修订</h4>
              <SourceHistory
                items={revisions}
                selectedId={activeRevision?.id || ''}
                currentId={status.data.current_revision_id}
                onSelect={(value) => {
                  setSelected(value)
                  setTestId('')
                  setNotice('已选择历史配置，请在测试与启用中测试后应用')
                }}
              />
              {history.hasNextPage && (
                <Button
                  className='mt-3'
                  variant='outline'
                  disabled={history.isFetchingNextPage}
                  onClick={() => void history.fetchNextPage()}
                >
                  加载更多修订
                </Button>
              )}
            </section>
            {activeRevision && (
              <section className='border-t pt-5'>
                <h4 className='mb-3 text-sm font-medium'>账号密码</h4>
                <CredentialReveal
                  channelId={id}
                  revisionId={activeRevision.id}
                  allowed={admin}
                />
              </section>
            )}
            <section className='border-t pt-5'>
              <h4 className='mb-3 text-sm font-medium'>运行诊断</h4>
              <ChannelStatus value={status.data} />
              <details className='mt-4 text-sm'>
                <summary className='cursor-pointer text-muted-foreground'>
                  配置与任务信息
                </summary>
                <p className='mt-3'>
                  当前配置版本：
                  {current?.number ||
                    (status.data.current_revision_id
                      ? '请加载更多历史记录'
                      : '未配置')}
                </p>
                <div className='mt-3 grid gap-2 text-xs text-muted-foreground'>
                  {(['main', 'sub', 'recording'] as const).map((kind) => (
                    <p key={kind}>
                      {{ main: '主流', sub: '子流', recording: '录像' }[kind]}：
                      {status.data[kind].reason || '无附加诊断'}
                    </p>
                  ))}
                </div>
                {job.data && (
                  <p className='mt-2 font-mono text-xs break-all'>
                    任务编号：{job.data.id}
                  </p>
                )}
              </details>
            </section>
          </div>
        </Card>
      )}
      {status.data.current_revision_id && !current ? (
        <p className='px-1 text-sm'>
          当前修订在更早的历史中，请先加载更多修订。
        </p>
      ) : (
        <SourceForm
          channel={{ id, version }}
          revision={current}
          history={revisions}
          testPanel={
            activeRevision ? (
              <SourceTestControls
                key={activeRevision.id}
                channelId={id}
                version={version}
                revisionId={activeRevision.id}
                revisionNumber={activeRevision.number}
                proof={proof.data || null}
                first={status.data.requires_initial_recording_mode}
                onAccepted={accepted}
                busy={connecting}
              />
            ) : null
          }
          onSaved={async (value, signal, context) => {
            setConnecting(context.action === 'connect')
            setSelected(value)
            setTestId('')
            setJobId('')
            setError('')
            setNotice('')
            onConfigured(id)
            try {
              if (context.action === 'save') {
                void client.invalidateQueries()
                return '已保存，未启用；启用请点「保存并启用」'
              }
              return await connectSource(
                id,
                value.id,
                signal,
                context,
                (result) => {
                  if (!signal.aborted) setTestId(result.test_id || '')
                }
              )
            } finally {
              if (!signal.aborted) {
                setConnecting(false)
                void client.invalidateQueries()
              }
            }
          }}
        />
      )}
    </>
  )
}

// Name and group are business attributes: saving them must not rebuild the
// stream, so they travel in one PATCH and never touch the source revision.
// The panel stays open after saving: its own notice reports the outcome, and
// collapsing it would unmount the very confirmation the operator is reading.
function ChannelBasics({
  channel,
  done,
}: {
  channel: Schema<'ChannelSummary'>
  done: () => void
}) {
  const action = useAction()
  return (
    <>
      <Notices error={action.error} notice={action.notice} />
      <form
        className='grid gap-4 md:grid-cols-2'
        onSubmit={(event) => {
          event.preventDefault()
          const data = fields(event.currentTarget)
          void action.run(
            () =>
              apiRequest(
                `/api/v1/channels/${channel.channel_id}`,
                jsonRequest(
                  'PATCH',
                  {
                    channel_name: text(data, 'channel_name'),
                    channel_group: text(data, 'channel_group'),
                  },
                  channel.version
                )
              ),
            '基本信息已保存'
          )
        }}
      >
        <Field
          label='通道名称'
          name='channel_name'
          defaultValue={channel.channel_name}
          required
          maxLength={128}
        />
        <Field
          label='分组（可留空）'
          name='channel_group'
          defaultValue={channel.channel_group}
          maxLength={64}
          placeholder='例如 一层 / 外围'
        />
        <div className='flex flex-wrap items-center justify-between gap-3 border-t pt-4 md:col-span-2'>
          <span className='text-xs text-muted-foreground'>
            改名与分组不会重建媒体流
          </span>
          <div className='flex gap-2'>
            <Button
              type='button'
              variant='ghost'
              disabled={action.pending}
              onClick={done}
            >
              取消
            </Button>
            <Button variant='outline' disabled={action.pending}>
              保存基本信息
            </Button>
          </div>
        </div>
      </form>
    </>
  )
}

function ChannelEnableToggle({
  channel,
  version,
}: {
  channel: Schema<'ChannelSummary'>
  version: number
}) {
  const action = useAction()
  const [confirmDisable, setConfirmDisable] = useState(false)
  return (
    <>
      {channel.enabled ? (
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={action.pending}
          onClick={() => setConfirmDisable(true)}
        >
          停用
        </Button>
      ) : (
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={action.pending}
          onClick={() =>
            void action.run(
              () =>
                apiRequest(
                  `/api/v1/channels/${channel.channel_id}`,
                  jsonRequest('PATCH', { enabled: true }, version)
                ),
              '通道已启用；取流与录像会在下一次调度时恢复'
            )
          }
        >
          启用
        </Button>
      )}
      <ConfirmDialog
        open={confirmDisable}
        onOpenChange={setConfirmDisable}
        title='停用此通道？'
        desc='停用后该通道停止取流与录像，录像策略、摄像头配置与历史录像都保留。约 30 秒内生效。'
        confirmText='停用'
        cancelBtnText='取消'
        destructive
        isLoading={action.pending}
        handleConfirm={() => {
          setConfirmDisable(false)
          void action.run(
            () =>
              apiRequest(
                `/api/v1/channels/${channel.channel_id}`,
                jsonRequest('PATCH', { enabled: false }, version)
              ),
            '通道已停用；取流与录像会在约 30 秒内停止，历史录像保留'
          )
        }}
      />
    </>
  )
}
