import { useEffect, useRef, useState } from 'react'
import { apiRequest, jsonRequest, newRequestKey } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Field, Notices } from '@/features/foundation/ui'

type Props = {
  channel: Schema<'Channel'>
  revision: Schema<'SourceRevision'> | null
  history: Schema<'SourceRevision'>[]
  onSaved: (value: Schema<'SourceRevision'>) => void
}
export function SourceForm(props: Props) {
  return <SourceEditor key={props.channel.id} {...props} />
}
function SourceEditor({ channel, revision, history, onSaved }: Props) {
  const [passwordAction, setPasswordAction] = useState<
    Schema<'CredentialInput'>['password_action']
  >(revision ? 'keep' : 'replace')
  const [intent, setIntent] = useState<Schema<'DraftInput'>['identity_intent']>(
    revision ? 'modify' : 'replace'
  )
  const [intentRevision, setIntentRevision] = useState(revision?.id)
  if (intentRevision !== revision?.id) {
    setIntentRevision(revision?.id)
    setPasswordAction(revision ? 'keep' : 'replace')
    setIntent(revision ? 'modify' : 'replace')
  }
  const [pending, setPending] = useState(false),
    [error, setError] = useState(''),
    [notice, setNotice] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])
  const identities = history.filter(
    (entry, index, list) =>
      list.findIndex((value) => value.source_id === entry.source_id) === index
  )
  return (
    <section className='grid gap-4'>
      <Notices error={error} notice={notice} />
      <p className='text-sm text-muted-foreground'>
        修改配置沿用摄像头身份；更换摄像头建立新身份。通道授权和历史录像保留。保存草稿不会切换取流。
      </p>
      <form
        key={revision?.id || 'empty'}
        className='grid gap-4 md:grid-cols-2'
        onSubmit={async (event) => {
          event.preventDefault()
          if (pending) return
          const data = new FormData(event.currentTarget),
            value = (name: string) => String(data.get(name) || '')
          const password = value('password')
          if (passwordAction === 'replace' && /^[*•●]{3,}$/u.test(password)) {
            setError('请输入真实密码，不能保存掩码')
            return
          }
          const credentials: Schema<'CredentialInput'> = {
            password_action: passwordAction,
          }
          if (data.get('update_username'))
            credentials.username = value('username')
          if (passwordAction === 'replace') credentials.password = password
          const config: Schema<'SourceConfigInput'> = {
            ip: value('ip'),
            rtsp_port: Number(value('rtsp_port') || 554),
            main_path: value('main_path'),
            sub_path: value('sub_path'),
            transport: value('transport') as 'tcp' | 'udp',
          }
          if (value('onvif_port'))
            config.onvif_port = Number(value('onvif_port'))
          const input: Schema<'DraftInput'> = {
            config,
            credentials,
            identity_intent: intent,
          }
          if (intent === 'history')
            input.history_source_id = value('history_source_id')
          const abort = new AbortController()
          controller.current = abort
          setPending(true)
          setError('')
          setNotice('')
          try {
            const init = jsonRequest('POST', input, channel.version)
            const result = await apiRequest<Schema<'SourceRevision'>>(
              `/api/v1/channels/${channel.id}/source-revisions`,
              {
                ...init,
                headers: {
                  ...init.headers,
                  'Idempotency-Key': newRequestKey(),
                },
                signal: abort.signal,
              }
            )
            if (!abort.signal.aborted && result.channel_id === channel.id) {
              onSaved(result)
              setNotice('草稿已保存，请先测试，再应用')
            }
          } catch (e) {
            if (!abort.signal.aborted)
              setError(e instanceof Error ? e.message : '保存失败')
          } finally {
            if (!abort.signal.aborted) setPending(false)
          }
        }}
      >
        <Field
          label='通道编号'
          name='channel_no'
          value={channel.channel_no}
          readOnly
        />
        <Field
          label='IP 地址'
          name='ip'
          defaultValue={revision?.config.ip || ''}
          required
          placeholder='192.168.33.20'
        />
        <Field
          label='RTSP 端口'
          name='rtsp_port'
          type='number'
          min={1}
          max={65535}
          defaultValue={revision?.config.rtsp_port || 554}
          required
        />
        <Field
          label='ONVIF 端口（可选）'
          name='onvif_port'
          type='number'
          min={1}
          max={65535}
          defaultValue={revision?.config.onvif_port || ''}
        />
        <Field
          label='主流路径'
          name='main_path'
          defaultValue={revision?.config.main_path || '/main'}
          required
        />
        <Field
          label='子流路径'
          name='sub_path'
          defaultValue={revision?.config.sub_path || ''}
          placeholder='/sub'
        />
        <label className='grid gap-2 text-sm'>
          传输方式
          <select
            aria-label='传输方式'
            name='transport'
            defaultValue={revision?.config.transport || 'tcp'}
            className='rounded-md border bg-background p-2'
          >
            <option value='tcp'>TCP</option>
            <option value='udp'>UDP</option>
          </select>
        </label>
        <label className='grid gap-2 text-sm'>
          身份意图
          <select
            aria-label='身份意图'
            value={intent}
            onChange={(e) => setIntent(e.target.value as typeof intent)}
            className='rounded-md border bg-background p-2'
          >
            {revision && <option value='modify'>修改当前摄像头配置</option>}
            <option value='replace'>更换摄像头</option>
            {identities.length > 0 && (
              <option value='history'>恢复历史摄像头身份</option>
            )}
          </select>
        </label>
        {intent === 'history' && (
          <label className='grid gap-2 text-sm'>
            历史摄像头
            <select
              aria-label='历史摄像头'
              name='history_source_id'
              className='rounded-md border bg-background p-2'
            >
              {identities.map((value) => (
                <option key={value.source_id} value={value.source_id}>
                  身份 {value.source_id.slice(0, 8)} · 修订 {value.number}
                </option>
              ))}
            </select>
          </label>
        )}
        <div className='grid gap-2'>
          <label className='flex gap-2 text-sm'>
            <input
              type='checkbox'
              name='update_username'
              defaultChecked={!revision}
            />
            更新用户名（不勾选则保留）
          </label>
          <Field label='用户名' name='username' autoComplete='off' />
        </div>
        <label className='grid gap-2 text-sm'>
          密码处理
          <select
            aria-label='密码处理'
            value={passwordAction}
            onChange={(e) =>
              setPasswordAction(e.target.value as typeof passwordAction)
            }
            className='rounded-md border bg-background p-2'
          >
            {revision && <option value='keep'>保留当前密码</option>}
            <option value='replace'>输入新密码</option>
            <option value='clear'>清空密码</option>
          </select>
        </label>
        {passwordAction === 'replace' && (
          <Field
            label='新密码'
            name='password'
            type='password'
            autoComplete='new-password'
          />
        )}
        <div className='md:col-span-2'>
          <Button disabled={pending}>
            {pending ? '正在保存…' : '保存草稿'}
          </Button>
        </div>
      </form>
    </section>
  )
}
