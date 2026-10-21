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
  email: '邮件（SMTP）',
  wecom: '企业微信',
}

const KIND_HELP: Record<string, string> = {
  inapp: '事件页内的站内提醒，无需配置，不受冷却限制——每一次命中都会记下来。',
  webhook: '把通知内容 POST 到该地址，带签名与稳定投递 ID，接收端可据此去重。',
  email: '通过你自己的 SMTP 服务器发送邮件。参数顺序与常见 NVR 的「Email」页面一致：先填发件人，再填服务器与认证，最后登记收件人。',
  wecom: '在值班群里添加一个群机器人，把它的 Webhook 地址粘贴到这里。机器人地址里带着密钥，请当作密码保管。',
}

type RecipientDraft = { name: string; address: string }

type EmailDraft = {
  sender_name: string
  sender_address: string
  smtp_host: string
  smtp_port: string
  smtp_tls: boolean
  smtp_auth: boolean
  username: string
}

const EMPTY_EMAIL: EmailDraft = {
  sender_name: '',
  sender_address: '',
  smtp_host: '',
  // 25 is the plain-relay default a Hikvision page ships with; 587 is the
  // submission port most providers want. 25 is the safer prefill: the operator
  // who needs 587 knows it, and one who does not is on a local relay.
  smtp_port: '25',
  smtp_tls: false,
  smtp_auth: false,
  username: '',
}

const LAST_CHECK_TONES: Record<string, string> = {
  healthy: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
  unavailable: 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400',
}

const EMPTY_FORM = {
  kind: 'webhook',
  name: '',
  url: '',
  secret: '',
  enabled: true,
  email: EMPTY_EMAIL,
  recipients: [] as RecipientDraft[],
  wecom: { msgType: 'markdown', mentions: '' },
}

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
    const body: Record<string, unknown> = {
      kind: form.kind,
      name: form.name,
      // Both pushing kinds carry a URL: a webhook's endpoint, a group robot's
      // keyed address. The inbox and email have none, and sending one would be
      // refused by the server's own constraint rather than silently ignored.
      url: form.kind === 'webhook' || form.kind === 'wecom' ? form.url : '',
      secret: form.secret,
      enabled: form.enabled,
    }
    if (form.kind === 'wecom') {
      body.wecom = {
        wecom_msg_type: form.wecom.msgType,
        wecom_mentions: form.wecom.mentions,
      }
    }
    if (form.kind === 'email') {
      body.email = {
        sender_name: form.email.sender_name,
        sender_address: form.email.sender_address,
        smtp_host: form.email.smtp_host,
        smtp_port: Number(form.email.smtp_port || 0),
        smtp_tls: form.email.smtp_tls,
        smtp_auth: form.email.smtp_auth,
        username: form.email.username,
      }
      // Rows the operator left blank are not sent: an empty row is not a
      // mailbox, and sending it would be refused as an invalid address.
      body.recipients = form.recipients
        .filter((row) => row.address.trim() !== '')
        .map((row) => ({ name: row.name.trim(), address: row.address.trim() }))
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
    const email = channel.email
    setForm({
      kind: channel.kind ?? 'webhook',
      name: channel.name ?? '',
      url: channel.url ?? '',
      secret: '',
      enabled: channel.enabled ?? true,
      email: email
        ? {
            sender_name: email.sender_name ?? '',
            sender_address: email.sender_address ?? '',
            smtp_host: email.smtp_host ?? '',
            smtp_port: String(email.smtp_port ?? 25),
            smtp_tls: !!email.smtp_tls,
            smtp_auth: !!email.smtp_auth,
            username: email.username ?? '',
          }
        : EMPTY_EMAIL,
      recipients: (channel.recipients ?? []).map((row) => ({
        name: row.name ?? '',
        address: row.address ?? '',
      })),
      wecom: {
        msgType: channel.wecom?.wecom_msg_type ?? 'markdown',
        mentions: channel.wecom?.wecom_mentions ?? '',
      },
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

  // Testing one mailbox, because a refused address is a fact about that address.
  // A channel-level verdict would leave the operator guessing which one failed.
  function testRecipient(channel: Channel, recipientID: string) {
    void run(async () => {
      const result = await apiRequest<{ state?: string; address?: string; status?: number; error?: string }>(
        `/api/v1/notification-channels/${channel.id}/recipients/${recipientID}/test`,
        jsonRequest('POST')
      )
      const parts = [result.state === 'healthy' ? '已送达' : '未送达']
      if (result.address) parts.unshift(result.address)
      if (result.status) parts.push(`HTTP ${result.status}`)
      if (result.error) parts.push(result.error)
      setTestResult(parts.join(' · '))
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
        {form.kind === 'wecom' && (
          <>
            <label className='grid gap-2 text-sm md:col-span-2'>
              <span>群机器人 Webhook</span>
              <Input
                aria-label='企业微信机器人地址'
                type='url'
                value={form.url}
                onChange={(event) => setForm({ ...form, url: event.target.value })}
                required
                placeholder='https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...'
              />
              <span className='text-[11px] text-muted-foreground'>
                群设置 → 群机器人 → 添加机器人，复制它给的地址。地址里的 key 就是凭据。
              </span>
            </label>
            <label className='grid gap-2 text-sm'>
              <span>消息格式</span>
              <SelectField
                ariaLabel='企业微信消息格式'
                value={form.wecom.msgType}
                onValueChange={(next) =>
                  setForm({ ...form, wecom: { ...form.wecom, msgType: next || 'markdown' } })
                }
                options={[
                  { value: 'markdown', label: 'Markdown 文本' },
                  { value: 'news', label: '图文卡片' },
                ]}
              />
            </label>
            <label className='grid gap-2 text-sm'>
              <span>@ 手机号（可留空）</span>
              <Input
                aria-label='企业微信提醒手机号'
                value={form.wecom.mentions}
                onChange={(event) =>
                  setForm({ ...form, wecom: { ...form.wecom, mentions: event.target.value } })
                }
                placeholder='13800000000,13900000000'
              />
              <span className='text-[11px] text-muted-foreground'>
                留空则不 @ 任何人——每次都 @ 全体，值班群很快就会把机器人静音。
              </span>
            </label>
          </>
        )}
        {form.kind === 'email' && (
          <>
            {/* Order follows the operator's existing mental model: a Hikvision
                Email tab reads sender, server, then authentication. Reordering
                for "cleaner layout" would make them hunt. */}
            <label className='grid gap-2 text-sm'>
              <span>发件人名称</span>
              <Input
                aria-label='发件人名称'
                value={form.email.sender_name}
                onChange={(event) =>
                  setForm({ ...form, email: { ...form.email, sender_name: event.target.value } })
                }
                maxLength={128}
                placeholder='门岗值班'
              />
            </label>
            <label className='grid gap-2 text-sm'>
              <span>发件人地址</span>
              <Input
                aria-label='发件人地址'
                type='email'
                value={form.email.sender_address}
                onChange={(event) =>
                  setForm({ ...form, email: { ...form.email, sender_address: event.target.value } })
                }
                required
                placeholder='nvr@example.com'
              />
            </label>
            <label className='grid gap-2 text-sm'>
              <span>SMTP 服务器</span>
              <Input
                aria-label='SMTP 服务器'
                value={form.email.smtp_host}
                onChange={(event) =>
                  setForm({ ...form, email: { ...form.email, smtp_host: event.target.value } })
                }
                required
                placeholder='smtp.example.com'
              />
            </label>
            <label className='grid gap-2 text-sm'>
              <span>SMTP 端口</span>
              <Input
                aria-label='SMTP 端口'
                type='number'
                min={1}
                max={65535}
                value={form.email.smtp_port}
                onChange={(event) =>
                  setForm({ ...form, email: { ...form.email, smtp_port: event.target.value } })
                }
                required
              />
              <span className='text-[11px] text-muted-foreground'>
                465 一般是 SSL；587 是 STARTTLS（服务器支持时会自动升级）
              </span>
            </label>
            <CheckboxField
              label='启用 SSL'
              checked={form.email.smtp_tls}
              onCheckedChange={(checked) =>
                setForm({ ...form, email: { ...form.email, smtp_tls: checked === true } })
              }
            />
            <CheckboxField
              label='服务器认证'
              checked={form.email.smtp_auth}
              onCheckedChange={(checked) =>
                setForm({ ...form, email: { ...form.email, smtp_auth: checked === true } })
              }
            />
            {form.email.smtp_auth && (
              <>
                <label className='grid gap-2 text-sm'>
                  <span>用户名</span>
                  <Input
                    aria-label='SMTP 用户名'
                    value={form.email.username}
                    onChange={(event) =>
                      setForm({ ...form, email: { ...form.email, username: event.target.value } })
                    }
                    required
                    autoComplete='username'
                  />
                </label>
                <label className='grid gap-2 text-sm'>
                  <span>密码{editing ? '（留空保持不变）' : ''}</span>
                  <Input
                    aria-label='SMTP 密码'
                    type='password'
                    value={form.secret}
                    onChange={(event) => setForm({ ...form, secret: event.target.value })}
                    required={!editing}
                    autoComplete='new-password'
                  />
                </label>
              </>
            )}
          </>
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
      {form.kind === 'email' && (
        <div className='rounded-xl border bg-card p-4'>
          <div className='mb-2 flex items-center justify-between'>
            <h3 className='text-sm font-medium'>收件人</h3>
            <Button
              type='button'
              size='sm'
              variant='outline'
              onClick={() =>
                setForm({ ...form, recipients: [...form.recipients, { name: '', address: '' }] })
              }
            >
              添加一行
            </Button>
          </div>
          {/* The three rows the operator's page shows are the minimum: an empty
              list is legal in the editor (they may still be typing) but the
              server refuses to save a channel with no mailbox. */}
          {form.recipients.length === 0 && (
            <p className='mb-2 text-xs text-muted-foreground'>
              至少需要一个收件人；保存前请填写地址。
            </p>
          )}
          <div className='divide-y rounded-lg border'>
            {form.recipients.map((row, index) => (
              <div key={index} className='flex flex-wrap items-center gap-2 p-2'>
                <span className='w-6 text-center text-xs tabular-nums text-muted-foreground'>
                  {index + 1}
                </span>
                <Input
                  aria-label={`收件人名称 ${index + 1}`}
                  className='w-40'
                  placeholder='名称（可留空）'
                  maxLength={64}
                  value={row.name}
                  onChange={(event) => {
                    const next = [...form.recipients]
                    next[index] = { ...row, name: event.target.value }
                    setForm({ ...form, recipients: next })
                  }}
                />
                <Input
                  aria-label={`收件人地址 ${index + 1}`}
                  className='flex-1'
                  type='email'
                  placeholder='name@example.com'
                  value={row.address}
                  onChange={(event) => {
                    const next = [...form.recipients]
                    next[index] = { ...row, address: event.target.value }
                    setForm({ ...form, recipients: next })
                  }}
                />
                <Button
                  type='button'
                  size='sm'
                  variant='outline'
                  className='border-red-500/30 text-red-600 dark:text-red-400'
                  onClick={() =>
                    setForm({
                      ...form,
                      recipients: form.recipients.filter((_, at) => at !== index),
                    })
                  }
                >
                  移除
                </Button>
              </div>
            ))}
          </div>
          <p className='mt-2 text-[11px] text-muted-foreground'>
            保存后可在下方渠道卡片里对每个收件人单独测试。
          </p>
        </div>
      )}
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
                    <code
                      className='max-w-64 truncate rounded border bg-muted px-2 py-0.5 font-mono text-xs text-muted-foreground'
                      title={
                        channel.kind === 'wecom'
                          ? '机器人地址含密钥，鼠标悬停可见完整值'
                          : undefined
                      }
                    >
                      {/* A robot URL carries its key in the query string: it is a
                          credential, and a shoulder-surfer reading the list should
                          not get a working one. Webhook endpoints are shown as-is
                          because they are not secret on their own. */}
                      {channel.kind === 'wecom' ? maskKey(channel.url) : channel.url}
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
              {channel.wecom && (
                <p className='mt-2 text-xs text-muted-foreground'>
                  {channel.wecom.wecom_msg_type === 'news' ? '图文卡片' : 'Markdown 文本'}
                  {channel.wecom.wecom_mentions ? ` · @ ${channel.wecom.wecom_mentions}` : ' · 不 @ 人'}
                </p>
              )}
              {channel.email && (
                <p className='mt-2 text-xs text-muted-foreground'>
                  {channel.email.sender_address} · {channel.email.smtp_host}:
                  {channel.email.smtp_port}
                  {channel.email.smtp_tls ? ' · SSL' : ''}
                  {channel.email.smtp_auth ? ' · 认证' : ' · 不认证'}
                </p>
              )}
              {!!channel.recipients?.length && (
                <div className='mt-2 space-y-1'>
                  {channel.recipients.map((recipient, index) => {
                    const state = (recipient.last_check as { state?: string } | null | undefined)
                      ?.state
                    return (
                      <div
                        key={recipient.id}
                        data-testid='notification-recipient'
                        className='flex flex-wrap items-center gap-2 text-xs'
                      >
                        <span className='w-5 text-center tabular-nums text-muted-foreground'>
                          {index + 1}
                        </span>
                        <span className='w-32 truncate'>{recipient.name || '—'}</span>
                        <span className='flex-1 truncate font-mono text-muted-foreground'>
                          {recipient.address}
                        </span>
                        {state && (
                          <span
                            className={`rounded-full border px-2 py-0.5 ${
                              state === 'healthy'
                                ? 'border-emerald-500/30 text-emerald-600 dark:text-emerald-400'
                                : 'border-amber-500/30 text-amber-600 dark:text-amber-400'
                            }`}
                          >
                            {state === 'healthy' ? '上次测试成功' : '上次测试失败'}
                          </span>
                        )}
                        <Button
                          type='button'
                          size='sm'
                          variant='outline'
                          disabled={pending}
                          onClick={() => testRecipient(channel, recipient.id ?? '')}
                        >
                          测试
                        </Button>
                      </div>
                    )
                  })}
                </div>
              )}
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

/**
 * maskKey hides the query string of a robot URL.
 *
 * The whole URL is shown on hover and in the editor (the operator needs it to fix
 * a typo), but the list — which stays on screen — shows only the host. A robot key
 * is the only thing standing between a passer-by and the duty group.
 */
function maskKey(url: string): string {
  const [base] = url.split('?')
  return url.includes('?') ? base + '?key=••••' : base
}
