import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth-store'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useAPI } from '@/features/foundation/hooks'
import { Empty, Notices, QueryState, SelectField } from '@/features/foundation/ui'

type Target = Schema<'ArchiveTarget'>
type Capabilities = Schema<'ArchiveTargetCapabilities'>
type Policy = Schema<'ArchivePolicy'>

function errorText(error: unknown): string {
  if (error instanceof ApiError) {
    const texts: Record<string, string> = {
      archive_target_referenced: '该目标已有归档副本，只能停用不能删除',
      archive_target_unavailable: '归档目标当前不可达',
      archive_target_unauthorized: '归档目标拒绝当前凭据',
    }
    if (texts[error.code]) return texts[error.code]
    return error.message
  }
  return error instanceof Error ? error.message : '操作失败'
}

const EMPTY_FORM = { name: '', base_url: '', remote_root: '', username: '', password: '', cloud_retention_days: 365 }

/**
 * ARC-01: the WebDAV archive target list. Admin only — this is site
 * infrastructure, and the upload grant is a separate per-channel decision.
 */
export function ArchiveTargets() {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const client = useQueryClient()
  const targets = useAPI<PageData<Target>>('/api/v1/archive-targets', admin)
  const channels = useAPI<PageData<Schema<'Channel'>>>('/api/v1/channels?limit=100', admin)
  const policies = useAPI<{ items: Policy[] }>('/api/v1/archive-policies', admin)
  const site = useAPI<Schema<'Site'>>('/api/v1/site', admin)
  const [form, setForm] = useState(EMPTY_FORM)
  const [editing, setEditing] = useState<string>('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [caps, setCaps] = useState<Capabilities | null>(null)
  const [pending, setPending] = useState(false)

  if (!admin)
    return <Empty>归档目标只对管理员开放。</Empty>

  function refresh() {
    void client.invalidateQueries()
  }

  async function run(action: () => Promise<void>, message: string) {
    setPending(true)
    setError('')
    setNotice('')
    try {
      await action()
      setNotice(message)
      refresh()
    } catch (cause) {
      setError(errorText(cause))
    } finally {
      setPending(false)
    }
  }

  function submit() {
    void run(async () => {
      if (editing) {
        await apiRequest(`/api/v1/archive-targets/${editing}`, jsonRequest('PATCH', form))
        setNotice('归档目标已更新')
      } else {
        await apiRequest('/api/v1/archive-targets', jsonRequest('POST', form))
      }
      setForm(EMPTY_FORM)
      setEditing('')
    }, editing ? '归档目标已更新' : '归档目标已登记')
  }

  function test() {
    void run(async () => {
      const result = await apiRequest<Capabilities>('/api/v1/archive-targets/test', jsonRequest('POST', editing ? { id: editing, ...form } : form))
      setCaps(result)
    }, '')
  }

  function edit(target: Target) {
    setEditing(target.id ?? '')
    setForm({
      name: target.name ?? '',
      base_url: target.base_url ?? '',
      remote_root: target.remote_root ?? '',
      username: target.username ?? '',
      password: '',
      cloud_retention_days: target.cloud_retention_days ?? 365,
    })
    setCaps(null)
  }

  return (
    <div className='space-y-4'>
      <div>
        <h1 className='text-2xl font-semibold tracking-tight'>归档</h1>
        <p className='mt-1 text-sm text-muted-foreground'>
          WebDAV 归档目标是录像完成后的异步上传去处，与目录存储池互不替代；密码加密存储，测试会验证写入、读取、Range 与删除能力。
        </p>
      </div>
      <QueryState pending={targets.isPending} error={targets.error} retry={targets.refetch} />
      <Notices notice={notice} error={error} />
      <form
        className='grid items-end gap-3 rounded-xl border bg-card p-4 md:grid-cols-3'
        onSubmit={(event) => {
          event.preventDefault()
          submit()
        }}
      >
        <label className='grid gap-2 text-sm'>
          <span>名称</span>
          <Input aria-label='归档目标名称' value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} required maxLength={64} />
        </label>
        <label className='grid gap-2 text-sm'>
          <span>WebDAV URL</span>
          <Input aria-label='WebDAV URL' type='url' value={form.base_url} onChange={(event) => setForm({ ...form, base_url: event.target.value })} required placeholder='https://cloud.example.com/dav' />
        </label>
        <label className='grid gap-2 text-sm'>
          <span>远端根目录</span>
          <Input aria-label='远端根目录' value={form.remote_root} onChange={(event) => setForm({ ...form, remote_root: event.target.value })} required placeholder='/one-nvr' />
        </label>
        <label className='grid gap-2 text-sm'>
          <span>账号</span>
          <Input aria-label='WebDAV 账号' value={form.username} onChange={(event) => setForm({ ...form, username: event.target.value })} required autoComplete='off' />
        </label>
        <label className='grid gap-2 text-sm'>
          <span>密码{editing ? '（留空保持不变）' : ''}</span>
          <Input aria-label='WebDAV 密码' type='password' value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} required={!editing} autoComplete='new-password' />
        </label>
        <SelectField
          label='云端保留天数'
          value={String(form.cloud_retention_days)}
          onValueChange={(next) => setForm({ ...form, cloud_retention_days: Number(next ?? 365) })}
          options={[365, 730, 1825].map((days) => ({ value: String(days), label: `${days} 天` }))}
        />
        <div className='flex flex-wrap items-center gap-2 md:col-span-3'>
          <Button type='submit' disabled={pending}>{editing ? '保存修改' : '登记目标'}</Button>
          <Button type='button' variant='outline' disabled={pending} onClick={test}>测试连接</Button>
          {editing && (
            <Button type='button' variant='ghost' onClick={() => { setEditing(''); setForm(EMPTY_FORM); setCaps(null) }}>取消编辑</Button>
          )}
          {caps && (
            <span className='text-xs text-muted-foreground' role='status'>
              能力：写入{caps.write ? '✓' : '✗'} · 读取{caps.read ? '✓' : '✗'} · Range{caps.range ? '✓' : '✗'} · 删除{caps.delete ? '✓' : '✗（到期清理不可用）'}
            </span>
          )}
        </div>
      </form>
      {admin && !!targets.data?.items.length && (
        <PolicyTable
          channels={channels.data?.items || []}
          targets={targets.data?.items || []}
          policies={policies.data?.items || []}
          zone={site.data?.timezone || ''}
        />
      )}
      {!targets.data?.items.length && !targets.isPending ? (
        <Empty>还没有归档目标。登记一个 WebDAV 目标后，可在录像管理页对已发布录像发起上传。</Empty>
      ) : (
        <div className='space-y-3'>
          {(targets.data?.items || []).map((target) => (
            <div key={target.id} className='space-y-1 rounded-xl border bg-card p-4 text-sm' data-testid='archive-target-row'>
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <span className='font-medium'>
                  {target.name}
                  <span className='ml-2 text-xs text-muted-foreground'>{target.enabled ? '已启用' : '已停用'}</span>
                </span>
                <div className='flex items-center gap-2'>
                  <Button size='sm' variant='outline' onClick={() => edit(target)}>编辑</Button>
                  <Button
                    size='sm'
                    variant='ghost'
                    onClick={() => void run(async () => {
                      await apiRequest(`/api/v1/archive-targets/${target.id}`, jsonRequest('PATCH', { ...target, password: '', enabled: !target.enabled }))
                    }, target.enabled ? '目标已停用' : '目标已启用')}
                  >
                    {target.enabled ? '停用' : '启用'}
                  </Button>
                  <Button
                    size='sm'
                    variant='ghost'
                    className='text-destructive'
                    onClick={() => void run(async () => {
                      await apiRequest(`/api/v1/archive-targets/${target.id}`, { method: 'DELETE' })
                    }, '目标已删除')}
                  >
                    删除
                  </Button>
                </div>
              </div>
              <p className='text-muted-foreground'>
                {target.base_url} · 根目录 {target.remote_root} · 账号 {target.username} · 云端保留 {target.cloud_retention_days} 天
              </p>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function PolicyTable({
  channels,
  targets,
  policies,
  zone,
}: {
  channels: Schema<'Channel'>[]
  targets: Target[]
  policies: Policy[]
  zone: string
}) {
  const client = useQueryClient()
  const [drafts, setDrafts] = useState<Record<string, Policy>>({})
  const [rowError, setRowError] = useState('')
  const byChannel = new Map(policies.map((policy) => [policy.channel_id ?? '', policy]))
  const enabled = targets.filter((target) => target.enabled)

  async function save(channelID: string) {
    const draft = drafts[channelID]
    if (!draft) return
    setRowError('')
    try {
      await apiRequest('/api/v1/archive-policies', jsonRequest('PUT', draft))
      setDrafts(({ [channelID]: _, ...rest }) => rest)
      await client.invalidateQueries()
    } catch (cause) {
      setRowError(errorText(cause))
    }
  }

  function draftFor(channelID: string): Policy {
    return (
      drafts[channelID] ||
      byChannel.get(channelID) || { channel_id: channelID, target_id: enabled[0]?.id ?? '', mode: 'manual', schedule: '' }
    )
  }

  return (
    <div className='space-y-3 rounded-xl border bg-card p-4'>
      <div>
        <h2 className='text-sm font-medium'>通道归档策略</h2>
        <p className='mt-1 text-xs text-muted-foreground'>
          立即上传＝分片发布即入队；定时上传＝按站点时区{zone ? `（${zone}）` : ''}每日时刻扫描补齐；手动＝只在录像管理页选中后上传。时刻如 02:00 或 02:00,14:30。
        </p>
      </div>
      {rowError && (
        <p role='alert' className='text-sm text-destructive'>
          {rowError}
        </p>
      )}
      <div className='space-y-2'>
        {channels.map((channel) => {
          const id = channel.id ?? ''
          const label = `CH${String(channel.channel_no ?? 0).padStart(2, '0')} ${channel.channel_name}`
          const draft = draftFor(id)
          const dirty = drafts[id] !== undefined
          return (
            <div key={id} className='flex flex-wrap items-center gap-2 text-sm'>
              <span className='w-40 truncate'>{label}</span>
              <SelectField
                label={`${label} 归档目标`}
                value={draft.target_id ?? ''}
                onValueChange={(next) => setDrafts({ ...drafts, [id]: { ...draft, target_id: next ?? '' } })}
                options={enabled.map((target) => ({ value: target.id ?? '', label: target.name ?? '' }))}
                className='w-36'
              />
              <SelectField
                label={`${label} 上传模式`}
                value={draft.mode ?? 'manual'}
                onValueChange={(next) => setDrafts({ ...drafts, [id]: { ...draft, mode: (next ?? 'manual') as Policy['mode'] } })}
                options={[
                  { value: 'manual', label: '手动' },
                  { value: 'immediate', label: '立即上传' },
                  { value: 'scheduled', label: '定时上传' },
                ]}
                className='w-28'
              />
              {(draft.mode ?? 'manual') === 'scheduled' && (
                <Input
                  aria-label={`${label} 上传时刻`}
                  value={draft.schedule ?? ''}
                  onChange={(event) => setDrafts({ ...drafts, [id]: { ...draft, schedule: event.target.value } })}
                  placeholder='02:00'
                  className='w-28'
                />
              )}
              <Button size='sm' variant='outline' disabled={!dirty} onClick={() => void save(id)}>
                保存
              </Button>
            </div>
          )
        })}
      </div>
    </div>
  )
}
