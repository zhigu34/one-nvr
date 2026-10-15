import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { CheckboxField, Empty, Notices, QueryState, SelectField } from '@/features/foundation/ui'

type Channel = Schema<'NotificationChannel'>

const ERROR_CODES: Record<string, string> = {
  notification_channel_invalid: '渠道配置不完整：webhook 需要地址与签名密钥',
  notification_channel_in_use: '该渠道仍被规则使用，请先在规则里解除绑定',
  notification_channel_version_conflict: '渠道已被他人修改，请刷新后重试',
}

const errorText = (error: unknown) => apiErrorText(error, ERROR_CODES)

const KIND_LABELS: Record<string, string> = {
  inapp: '站内提醒',
  webhook: 'Webhook',
}

const KIND_HELP: Record<string, string> = {
  inapp: '事件页内的站内提醒，无需配置，不受冷却限制——每一次命中都会记下来。',
  webhook: '把通知内容 POST 到该地址，带签名与稳定投递 ID，接收端可据此去重。',
}

const LAST_CHECK_TONES: Record<string, string> = {
  healthy: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
  unavailable: 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400',
}

const EMPTY_FORM = { kind: 'webhook', name: '', url: '', secret: '', enabled: true }

/**
 * EVT-05: where an alert goes. The admin-only surface, because a channel names
 * an endpoint and holds a signing secret.
 *
 * The kind list comes from the server's adapter registry rather than a constant
 * here: a build that ships a new adapter must be able to offer it, and a UI that
 * hard-coded the kinds would silently omit it.
 */
export function NotificationChannels() {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const client = useQueryClient()
  const channels = useAPI<{ items: Channel[]; kinds: string[] }>(
    '/api/v1/notification-channels',
    admin
  )
  const [form, setForm] = useState(EMPTY_FORM)
  const [editing, setEditing] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [testResult, setTestResult] = useState('')
  const [pending, setPending] = useState(false)
  const [deleting, setDeleting] = useState<Channel | null>(null)
  // The version the row was read at, so an edit cannot silently overwrite a
  // change someone else made in between.
  const [editingVersion, setEditingVersion] = useState(0)

  if (!admin) return <Empty>通知渠道只对管理员开放。</Empty>

  function refresh() {
    void client.invalidateQueries()
  }

  async function run(action: () => Promise<void>, message: string) {
    setPending(true)
    setError('')
    setNotice('')
    try {
      await action()
      if (message) setNotice(message)
      refresh()
    } catch (cause) {
      setError(errorText(cause))
    } finally {
      setPending(false)
    }
  }

  function submit() {
    const body = {
      kind: form.kind,
      name: form.name,
      // The inbox has no endpoint; sending one would be refused by the server's
      // own constraint rather than silently ignored, so keep it out here.
      url: form.kind === 'webhook' ? form.url : '',
      secret: form.secret,
      enabled: form.enabled,
    }
    void run(
      async () => {
        if (editing) {
          await apiRequest(
            `/api/v1/notification-channels/${editing}`,
            jsonRequest('PATCH', body, editingVersion)
          )
        } else {
          await apiRequest('/api/v1/notification-channels', jsonRequest('POST', body))
        }
        setForm(EMPTY_FORM)
        setEditing('')
        setEditingVersion(0)
        setTestResult('')
      },
      editing ? '渠道已更新' : '渠道已添加'
    )
  }

  function edit(channel: Channel) {
    setEditing(channel.id ?? '')
    setEditingVersion(channel.version ?? 0)
    setForm({
      kind: channel.kind ?? 'webhook',
      name: channel.name ?? '',
      url: channel.url ?? '',
      secret: '',
      enabled: channel.enabled ?? true,
    })
    setTestResult('')
  }

  function test(channel: Channel) {
    void run(async () => {
      const result = await apiRequest<{ state?: string; status?: number; error?: string }>(
        `/api/v1/notification-channels/${channel.id}/test`,
        jsonRequest('POST')
      )
      // Show what the receiver actually answered. A test that only says
      // "failed" leaves the operator guessing at the other end.
      const parts = [result.state === 'healthy' ? '接收端已接受' : '接收端未接受']
      if (result.status) parts.push(`HTTP ${result.status}`)
      if (result.error) parts.push(result.error)
      setTestResult(`${channel.name}：${parts.join(' · ')}`)
    }, '')
  }

  const kinds = channels.data?.kinds?.length ? channels.data.kinds : ['inapp', 'webhook']

  return (
    <div className='space-y-4'>
      <div>
        <h2 className='text-lg font-semibold tracking-tight'>通知渠道</h2>
        <p className='mt-1 text-sm text-muted-foreground'>
          规则命中后在此投递：站内提醒始终记录，Webhook 按 5 次退避重试投递，失败不影响事件保存与录像。冷却只限制外部渠道的重复推送，站内与会话记录不会被冷却丢弃。
        </p>
      </div>
      <QueryState pending={channels.isPending} error={channels.error} retry={channels.refetch} />
      <Notices notice={notice} error={error} />
      {testResult && (
        <p role='status' className='rounded-lg border bg-muted/40 px-3 py-2 text-xs'>
          {testResult}
        </p>
      )}
      <form
        className='grid items-end gap-3 rounded-xl border bg-card p-4 md:grid-cols-3'
        onSubmit={(event) => {
          event.preventDefault()
          submit()
        }}
      >
        <label className='grid gap-2 text-sm'>
          <span>类型</span>
          <SelectField
            ariaLabel='渠道类型'
            value={form.kind}
            onValueChange={(next) => {
              setForm({ ...form, kind: next || 'webhook' })
              setTestResult('')
            }}
            options={kinds.map((kind) => ({ value: kind, label: KIND_LABELS[kind] || kind }))}
          />
        </label>
        <label className='grid gap-2 text-sm'>
          <span>名称</span>
          <Input
            aria-label='渠道名称'
            value={form.name}
            onChange={(event) => setForm({ ...form, name: event.target.value })}
            required
            maxLength={64}
            placeholder='值班群'
          />
        </label>
        {form.kind === 'webhook' && (
          <label className='grid gap-2 text-sm'>
            <span>Webhook 地址</span>
            <Input
              aria-label='Webhook 地址'
              type='url'
              value={form.url}
              onChange={(event) => setForm({ ...form, url: event.target.value })}
              required
              placeholder='https://chat.example.com/hook/xxx'
            />
          </label>
        )}
        {form.kind === 'webhook' && (
          <label className='grid gap-2 text-sm'>
            <span>签名密钥{editing ? '（留空保持不变）' : ''}</span>
            <Input
              aria-label='签名密钥'
              type='password'
              value={form.secret}
              onChange={(event) => setForm({ ...form, secret: event.target.value })}
              required={!editing}
              autoComplete='new-password'
            />
          </label>
        )}
        <CheckboxField
          label='启用'
          checked={form.enabled}
          onCheckedChange={(checked) => setForm({ ...form, enabled: checked === true })}
        />
        <div className='flex gap-2'>
          <Button type='submit' disabled={pending}>
            {editing ? '保存' : '添加'}
          </Button>
          {editing && (
            <Button
              type='button'
              variant='outline'
              onClick={() => {
                setEditing('')
                setEditingVersion(0)
                setForm(EMPTY_FORM)
                setTestResult('')
              }}
            >
              取消
            </Button>
          )}
        </div>
        <p className='text-xs text-muted-foreground md:col-span-3'>{KIND_HELP[form.kind]}</p>
      </form>
      {channels.data && channels.data.items.length === 0 && (
        <Empty>尚未添加通知渠道。添加站内提醒或 Webhook 后，规则即可绑定它们。</Empty>
      )}
      <div className='grid gap-3'>
        {(channels.data?.items || []).map((channel) => {
          const check = channel.last_check as { state?: string } | null | undefined
          const tone = check?.state ? LAST_CHECK_TONES[check.state] : ''
          return (
            <div key={channel.id} className='rounded-xl border bg-card p-4'>
              <div className='flex flex-wrap items-center justify-between gap-3'>
                <div className='flex flex-wrap items-center gap-2'>
                  <span className='font-medium'>{channel.name}</span>
                  <span className='rounded-full border px-2 py-0.5 text-xs'>
                    {KIND_LABELS[channel.kind ?? ''] || channel.kind}
                  </span>
                  {tone && check?.state && (
                    <span className={`rounded-full border px-2 py-0.5 text-xs ${tone}`}>
                      {check.state === 'healthy' ? '上次测试成功' : '上次测试失败'}
                    </span>
                  )}
                  {!channel.enabled && (
                    <span className='rounded-full border px-2 py-0.5 text-xs text-muted-foreground'>
                      已停用
                    </span>
                  )}
                </div>
                <div className='flex gap-2'>
                  {channel.url && (
                    <code className='max-w-64 truncate rounded border bg-muted px-2 py-0.5 font-mono text-xs text-muted-foreground'>
                      {channel.url}
                    </code>
                  )}
                  <Button type='button' size='sm' variant='outline' disabled={pending} onClick={() => test(channel)}>
                    测试
                  </Button>
                  <Button type='button' size='sm' variant='outline' disabled={pending} onClick={() => edit(channel)}>
                    编辑
                  </Button>
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    className='border-red-500/30 text-red-600 dark:text-red-400'
                    disabled={pending}
                    onClick={() => setDeleting(channel)}
                  >
                    删除
                  </Button>
                </div>
              </div>
              {channel.has_secret && (
                <p className='mt-2 text-xs text-muted-foreground'>已保存签名密钥（不回显）</p>
              )}
            </div>
          )
        })}
      </div>
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`删除通知渠道「${deleting?.name ?? ''}」？`}
        desc='仍被规则绑定的渠道无法删除。删除后该渠道的历史投递记录一并移除。'
        confirmText='删除'
        destructive
        isLoading={pending}
        handleConfirm={() => {
          const target = deleting
          setDeleting(null)
          if (!target) return
          void run(
            async () => {
              await apiRequest(
                `/api/v1/notification-channels/${target.id}`,
                jsonRequest('DELETE', undefined, target.version)
              )
            },
            '渠道已删除'
          )
        }}
      />
    </div>
  )
}
