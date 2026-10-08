import { useEffect, useState } from 'react'
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema, PageData } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAPI, useAction, fields, text } from '@/features/foundation/hooks'
import {
  PageTitle,
  Panel,
  Forbidden,
  QueryState,
  Notices,
  Field,
} from '@/features/foundation/ui'
import { sourceCommand } from './api'
import { ChannelStatus } from './channel-status'
import { connectSource } from './source-connect'
import { CredentialReveal } from './source-credentials'
import { SourceForm } from './source-form'
import { SourceHistory } from './source-history'
import { SourceTestControls } from './source-test'

export function ChannelConfigure({ initialId = '' }: { initialId?: string }) {
  const channels = useAPI<PageData<Schema<'Channel'>>>(
    '/api/v1/channels?limit=100',
    true,
    5000
  )
  const [selected, setSelected] = useState(initialId)
  const allowed =
    channels.data?.items.filter((channel) =>
      channel.permissions.includes('configure')
    ) || []
  const channel = allowed.find(
    (channel) => channel.id === (selected || allowed[0]?.id)
  )
  return (
    <>
      <PageTitle
        title='通道配置'
        description='选择通道，设置摄像头连接。录像方式在「录像计划」页管理。'
      />
      <QueryState
        pending={channels.isPending}
        error={channels.error}
        retry={() => channels.refetch()}
      />
      {!channels.isPending && !channels.error && allowed.length === 0 && (
        <Forbidden />
      )}
      {allowed.length > 0 && (
        <div className='grid items-start gap-5 lg:grid-cols-[220px_minmax(0,1fr)]'>
          <aside className='rounded-xl border bg-card p-3 lg:sticky lg:top-20'>
            <a
              href='/channels'
              className='mb-4 block px-1 text-sm text-muted-foreground hover:text-foreground'
            >
              ← 返回通道列表
            </a>
            <label className='grid gap-2 text-sm'>
              选择通道
              <select
                aria-label='选择通道'
                value={channel?.id || ''}
                onChange={(e) => setSelected(e.target.value)}
                className='rounded-md border bg-background p-2'
              >
                {allowed.map((value) => (
                  <option key={value.id} value={value.id}>
                    CH{String(value.channel_no).padStart(2, '0')} ·{' '}
                    {value.channel_name}
                  </option>
                ))}
              </select>
            </label>
            <nav
              aria-label='切换通道'
              className='mt-3 hidden max-h-[calc(100vh-16rem)] space-y-1 overflow-y-auto lg:block'
            >
              {allowed.map((value) => (
                <Button
                  key={value.id}
                  type='button'
                  variant={channel?.id === value.id ? 'secondary' : 'ghost'}
                  className='w-full justify-start gap-3'
                  aria-pressed={channel?.id === value.id}
                  onClick={() => setSelected(value.id)}
                >
                  <span className='font-mono text-xs'>
                    CH{String(value.channel_no).padStart(2, '0')}
                  </span>
                  <span className='truncate'>{value.channel_name}</span>
                </Button>
              ))}
            </nav>
          </aside>
          <div className='min-w-0'>
            {channel && <ConfigureChannel key={channel.id} channel={channel} />}
          </div>
        </div>
      )}
    </>
  )
}
function ConfigureChannel({ channel }: { channel: Schema<'Channel'> }) {
  const client = useQueryClient()
  const admin = useAuthStore((s) => s.user?.role === 'admin')
  const status = useAPI<Schema<'ChannelSourceStatus'>>(
    `/api/v1/channels/${channel.id}/source/status`,
    true,
    2000
  )
  const history = useInfiniteQuery({
    queryKey: [`/api/v1/channels/${channel.id}/source-revisions`],
    initialPageParam: '',
    queryFn: ({ pageParam, signal }) =>
      apiRequest<Schema<'RevisionPage'>>(
        `/api/v1/channels/${channel.id}/source-revisions?limit=100${pageParam ? '&cursor=' + pageParam : ''}`,
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
  const [testId, setTestId] = useState(''),
    [jobId, setJobId] = useState(''),
    [notice, setNotice] = useState(''),
    [error, setError] = useState(''),
    [clearChecked, setClearChecked] = useState(false),
    [pending, setPending] = useState(false)
  const proof = useAPI<Schema<'SourceTestResult'>>(
    `/api/v1/channels/${channel.id}/source-tests/${testId}`,
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
  const version = Math.max(channel.version, status.data.version)
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
      <div className='mb-5 flex flex-wrap items-center justify-between gap-4 rounded-xl border bg-card p-4'>
        <div>
          <span className='font-mono text-xs text-muted-foreground'>
            CH{String(channel.channel_no).padStart(2, '0')}
          </span>
          <h2 className='mt-1 text-lg font-semibold'>{channel.channel_name}</h2>
        </div>
        <ChannelStatus value={status.data} />
      </div>
      <Tabs defaultValue='connection'>
        <TabsList className='mb-3 h-auto flex-wrap'>
          <TabsTrigger value='connection'>连接配置</TabsTrigger>
          <TabsTrigger value='history'>历史与诊断</TabsTrigger>
        </TabsList>
        <TabsContent
          value='connection'
          forceMount
          className='data-[state=inactive]:hidden'
        >
          <Panel title='基本信息与连接'>
            <ChannelName channel={{ ...channel, version }} />
            <div className='mt-6 border-t pt-6'>
              {status.data.current_revision_id && !current ? (
                <p>当前修订在更早的历史中，请先加载更多修订。</p>
              ) : (
                <SourceForm
                  channel={{ ...channel, version }}
                  revision={current}
                  history={revisions}
                  onSaved={async (value, signal, context) => {
                    setConnecting(context.action === 'connect')
                    setSelected(value)
                    setTestId('')
                    setJobId('')
                    setError('')
                    setNotice('')
                    try {
                      if (context.action === 'save') {
                        void client.invalidateQueries()
                        return '已保存，未启用；启用请点「保存并启用」'
                      }
                      return await connectSource(
                        channel.id,
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
            </div>
          </Panel>
          {activeRevision && (
            <Panel title='测试与启用'>
              <SourceTestControls
                key={activeRevision.id}
                channelId={channel.id}
                version={version}
                revisionId={activeRevision.id}
                proof={proof.data || null}
                first={status.data.requires_initial_recording_mode}
                onAccepted={accepted}
                busy={connecting}
              />
              <QueryState
                pending={!!testId && proof.isPending}
                error={proof.error}
                retry={() => proof.refetch()}
              />
            </Panel>
          )}
          <div className='mb-6 flex flex-wrap items-center justify-between gap-3 rounded-xl border bg-card p-4'>
            <p className='text-sm'>
              录像方式（关闭 / 手动 / 定时 / 事件）与存储池在「录像计划」页设置。
            </p>
            <Button asChild variant='outline'>
              <a href='/recording-plan'>前往录像计划</a>
            </Button>
          </div>
        </TabsContent>
        <TabsContent value='history'>
          <Panel title='配置历史'>
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
            {activeRevision && (
              <div className='mt-4'>
                <CredentialReveal
                  channelId={channel.id}
                  revisionId={activeRevision.id}
                  allowed={admin}
                />
              </div>
            )}
          </Panel>
          <Panel title='运行诊断'>
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
          </Panel>
          <details className='mb-6 rounded-xl border border-destructive/30 bg-card p-4'>
            <summary className='cursor-pointer text-sm font-medium text-destructive'>
              清空摄像头配置
            </summary>
            <div className='mt-4'>
              <Panel title='清空当前摄像头'>
                <p className='mb-3 text-sm'>
                  清空会停止当前取流和录像，通道编号、权限及历史录像保留。
                </p>
                <label className='mb-3 flex gap-2 text-sm'>
                  <input
                    type='checkbox'
                    checked={clearChecked}
                    onChange={(e) => setClearChecked(e.target.checked)}
                  />
                  确认清空此通道摄像头
                </label>
                <Button
                  variant='destructive'
                  disabled={
                    pending || !clearChecked || !status.data.current_revision_id
                  }
                  onClick={async () => {
                    setPending(true)
                    setError('')
                    try {
                      const value = await sourceCommand<Schema<'SourceChange'>>(
                        `/api/v1/channels/${channel.id}/source/clear`,
                        {},
                        version
                      )
                      accepted(value)
                      setSelected(null)
                      setTestId('')
                      setClearChecked(false)
                    } catch (e) {
                      setError(e instanceof Error ? e.message : '清空失败')
                    } finally {
                      setPending(false)
                    }
                  }}
                >
                  清空摄像头
                </Button>
              </Panel>
            </div>
          </details>
        </TabsContent>
      </Tabs>
    </>
  )
}

function ChannelName({ channel }: { channel: Schema<'Channel'> }) {
  const action = useAction()
  return (
    <>
      <Notices {...action} />
      <form
        className='flex flex-wrap items-end gap-3'
        onSubmit={(event) => {
          event.preventDefault()
          const name = text(fields(event.currentTarget), 'channel_name')
          void action.run(
            () =>
              apiRequest(
                `/api/v1/channels/${channel.id}`,
                jsonRequest('PATCH', { channel_name: name }, channel.version)
              ),
            '名称已保存'
          )
        }}
      >
        <div className='min-w-0 flex-1'>
          <Field
            label='通道名称'
            name='channel_name'
            defaultValue={channel.channel_name}
            required
            maxLength={128}
          />
        </div>
        <Button variant='outline' disabled={action.pending}>
          保存名称
        </Button>
      </form>
    </>
  )
}
