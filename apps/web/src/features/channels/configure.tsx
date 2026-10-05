import { useEffect, useState } from 'react'
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest } from '@/lib/api-client'
import type { Schema, PageData } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI } from '@/features/foundation/hooks'
import {
  PageTitle,
  Panel,
  Forbidden,
  QueryState,
  Notices,
} from '@/features/foundation/ui'
import { sourceCommand } from './api'
import { ChannelStatus } from './channel-status'
import { RecordingPolicyControls } from './recording-policy'
import { RecordingsIndex } from './recordings-index'
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
        description='通道身份和历史录像持续保留，摄像头配置以修订管理。'
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
        <label className='mb-6 grid gap-2 text-sm'>
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
      )}
      {channel && <ConfigureChannel key={channel.id} channel={channel} />}
    </>
  )
}
function ConfigureChannel({ channel }: { channel: Schema<'Channel'> }) {
  const client = useQueryClient(),
    admin = useAuthStore((s) => s.user?.role === 'admin')
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
  const policy = useAPI<Schema<'RecordingPolicy'>>(
    `/api/v1/channels/${channel.id}/recording-policy`,
    true,
    2000
  )
  const pools = useAPI<PageData<Schema<'Pool'>>>(
    '/api/v1/storage-pools?limit=100',
    admin,
    5000
  )
  const [selected, setSelected] = useState<Schema<'SourceRevision'> | null>(
    null
  )
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
  if (history.error || status.error || policy.error)
    return (
      <QueryState
        pending={false}
        error={history.error || status.error || policy.error}
        retry={() => client.invalidateQueries()}
      />
    )
  if (!status.data || !history.data || !policy.data)
    return <QueryState pending error={null} />
  const version = Math.max(
    channel.version,
    status.data.version,
    policy.data.version
  )
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
      <Panel title='运行状态'>
        <ChannelStatus value={status.data} />
        <p className='mt-3 text-xs text-muted-foreground'>
          当前修订：
          {current
            ? current.number
            : status.data.current_revision_id
              ? '已配置（请加载更早修订）'
              : '未配置摄像头'}
        </p>
      </Panel>
      <Panel title='摄像头配置'>
        {status.data.current_revision_id && !current && !selected ? (
          <p>当前修订在更早的历史中，请先加载更多修订。</p>
        ) : (
          <SourceForm
            channel={{ ...channel, version }}
            revision={activeRevision}
            history={revisions}
            onSaved={(value) => {
              setSelected(value)
              setTestId('')
              void client.invalidateQueries()
            }}
          />
        )}
      </Panel>
      <Panel title='修订与取流测试'>
        <SourceHistory
          items={revisions}
          selectedId={activeRevision?.id || ''}
          currentId={status.data.current_revision_id}
          onSelect={(value) => {
            setSelected(value)
            setTestId('')
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
          <div className='mt-5 grid gap-4'>
            <SourceTestControls
              key={activeRevision.id}
              channelId={channel.id}
              version={version}
              revisionId={activeRevision.id}
              proof={proof.data || null}
              first={!status.data.current_revision_id}
              hasPool={!!status.data.storage_pool_id}
              onAccepted={accepted}
            />
            <QueryState
              pending={!!testId && proof.isPending}
              error={proof.error}
              retry={() => proof.refetch()}
            />
            <CredentialReveal
              channelId={channel.id}
              revisionId={activeRevision.id}
              allowed={admin}
            />
          </div>
        )}
      </Panel>
      <Panel title='录像与存储池'>
        <RecordingPolicyControls
          channel={{ ...channel, version }}
          mode={policy.data.mode}
          poolId={status.data.storage_pool_id}
          pools={pools.data?.items || []}
          onAccepted={accepted}
        />
      </Panel>
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
      {channel.permissions.includes('playback') && (
        <Panel title='录像索引'>
          <RecordingsIndex channelId={channel.id} />
        </Panel>
      )}
    </>
  )
}
