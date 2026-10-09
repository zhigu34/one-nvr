import { useEffect, useRef, useState } from 'react'
import { apiRequest, jsonRequest, newRequestKey } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Field, Notices } from '@/features/foundation/ui'
import { connectionError, type SaveConnection } from './source-connect'
import { SourceOnvifPanel } from './source-onvif'

type Props = {
  channel: Schema<'Channel'>
  revision: Schema<'SourceRevision'> | null
  history: Schema<'SourceRevision'>[]
  onSaved: (
    value: Schema<'SourceRevision'>,
    signal: AbortSignal,
    context: SaveConnection
  ) => void | string | Promise<void | string>
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
  // ONVIF is a manual add method now: the operator names the camera and the
  // panel asks it for its own stream configuration. The connection fields are
  // controlled so a probe can fill them, while every write still happens through
  // the same save / save-and-enable submission below.
  const [method, setMethod] = useState<'rtsp' | 'onvif'>('rtsp')
  const [ip, setIP] = useState(revision?.config.ip || '')
  const [rtspPort, setRTSPPort] = useState(
    String(revision?.config.rtsp_port || 554)
  )
  const [mainPath, setMainPath] = useState(
    revision?.config.main_path || '/main'
  )
  const [subPath, setSubPath] = useState(revision?.config.sub_path || '')
  const [onvifPort, setONVIFPort] = useState(
    revision?.config.onvif_port ? String(revision.config.onvif_port) : ''
  )
  const [probed, setProbed] = useState(false)
  const [intentRevision, setIntentRevision] = useState(revision?.id)
  const [password, setPassword] = useState('')
  const form = useRef<HTMLFormElement>(null)
  if (intentRevision !== revision?.id) {
    setIntentRevision(revision?.id)
    setPasswordAction(revision ? 'keep' : 'replace')
    setIntent(revision ? 'modify' : 'replace')
    setPassword('')
    setIP(revision?.config.ip || '')
    setRTSPPort(String(revision?.config.rtsp_port || 554))
    setMainPath(revision?.config.main_path || '/main')
    setSubPath(revision?.config.sub_path || '')
    setONVIFPort(
      revision?.config.onvif_port ? String(revision.config.onvif_port) : ''
    )
    setProbed(false)
  }
  // The probe uses what the operator is entering right now, so it reads the same
  // fields the submission will send instead of keeping a second copy.
  function enteredCredentials() {
    const element = form.current
    if (!element) return { username: '', password: '' }
    const data = new FormData(element)
    return {
      username: String(data.get('username') || ''),
      password: String(data.get('password') || ''),
    }
  }
  function applyStreams(
    main: Schema<'OnvifStream'> | null,
    sub: Schema<'OnvifStream'> | null
  ) {
    if (!main) {
      setProbed(false)
      return
    }
    setIP(main.ip)
    setRTSPPort(String(main.rtsp_port))
    setMainPath(main.path)
    setSubPath(sub ? sub.path : '')
    setProbed(true)
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
        「保存」只记录这条摄像头信息，不影响当前正在取的流；「保存并启用」才切换到新配置。
        录像方式在「录像计划」页设置。
      </p>
      <form
        key={revision?.id || 'empty'}
        ref={form}
        className='grid gap-4 md:grid-cols-2'
        onSubmit={async (event) => {
          event.preventDefault()
          if (pending) return
          setError('')
          setNotice('')
          const submitter = (event.nativeEvent as SubmitEvent)
            .submitter as HTMLButtonElement | null
          const action: SaveConnection['action'] =
            submitter?.value === 'save' ? 'save' : 'connect'
          const data = new FormData(event.currentTarget),
            value = (name: string) => String(data.get(name) || '')
          if (passwordAction === 'replace' && /^[*•●]{3,}$/u.test(password)) {
            setError('请输入真实密码，不能保存掩码')
            return
          }
          const credentials: Schema<'CredentialInput'> = {
            password_action: passwordAction,
          }
          if (value('username') || data.get('update_username'))
            credentials.username = value('username')
          if (passwordAction === 'replace') credentials.password = password
          const config: Schema<'SourceConfigInput'> = {
            ip: value('ip'),
            rtsp_port: Number(value('rtsp_port') || 554),
            main_path: value('main_path'),
            sub_path: value('sub_path'),
            transport: value('transport') as 'tcp' | 'udp',
          }
          // The ONVIF port belongs to this form now. It is carried forward from
          // the saved revision and only replaced when the operator sets one, so
          // data that came from an import is never silently dropped.
          if (onvifPort.trim() !== '') config.onvif_port = Number(onvifPort)
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
              const message = await onSaved(result, abort.signal, {
                action,
                version: channel.version + 1,
                progress: (message) => {
                  if (!abort.signal.aborted) setNotice(message)
                },
              })
              if (!abort.signal.aborted) setNotice(message || '配置已保存')
            }
          } catch (e) {
            if (!abort.signal.aborted) {
              setNotice('')
              setError(connectionError(e))
            }
          } finally {
            if (!abort.signal.aborted) setPending(false)
          }
        }}
      >
        <fieldset disabled={pending} className='contents'>
          <div className='grid gap-2 md:col-span-2'>
            <span className='text-sm'>添加方式</span>
            <div className='flex flex-wrap gap-2'>
              <button
                type='button'
                aria-pressed={method === 'rtsp'}
                onClick={() => setMethod('rtsp')}
                className={`rounded-md border px-4 py-2 text-sm ${
                  method === 'rtsp' ? 'bg-accent font-medium' : ''
                }`}
              >
                RTSP 手动
              </button>
              <button
                type='button'
                aria-pressed={method === 'onvif'}
                onClick={() => setMethod('onvif')}
                className={`rounded-md border px-4 py-2 text-sm ${
                  method === 'onvif' ? 'bg-accent font-medium' : ''
                }`}
              >
                ONVIF 手动
              </button>
            </div>
            <p className='text-xs text-muted-foreground'>
              {method === 'onvif'
                ? '填写地址与凭据后从摄像头读取码流配置，不用手填路径。'
                : '手动填写 RTSP 端口与主/子流路径。'}
            </p>
          </div>
          <Field
            label='IP 地址'
            name='ip'
            value={ip}
            onChange={(event) => setIP(event.target.value)}
            required
            placeholder='192.168.33.20'
          />
          {(method === 'rtsp' || probed) && (
            <Field
              label='RTSP 端口'
              name='rtsp_port'
              type='number'
              min={1}
              max={65535}
              value={rtspPort}
              onChange={(event) => setRTSPPort(event.target.value)}
              required
            />
          )}
          <Field
            label='用户名'
            name='username'
            autoComplete='off'
            placeholder={
              revision
                ? `已保存 ${revision.username_summary || '用户名'}，留空保留`
                : '摄像头用户名'
            }
          />
          {passwordAction !== 'clear' && (
            <Field
              label='密码'
              name='password'
              type='password'
              autoComplete='new-password'
              placeholder={revision ? '留空保留已保存的密码' : '摄像头密码'}
              value={password}
              onChange={(event) => {
                setPassword(event.target.value)
                setPasswordAction(
                  revision && !event.target.value ? 'keep' : 'replace'
                )
              }}
            />
          )}
          {method === 'onvif' && (
            <div className='md:col-span-2'>
              <SourceOnvifPanel
                channelId={channel.id}
                ip={ip}
                port={onvifPort}
                onPortChange={setONVIFPort}
                credentials={enteredCredentials}
                onStreams={applyStreams}
                savedPassword={passwordAction === 'keep' && !!revision}
                busy={pending}
              />
            </div>
          )}
          {(method === 'rtsp' || probed) && (
            <>
              <Field
                label='主流路径'
                name='main_path'
                value={mainPath}
                onChange={(event) => setMainPath(event.target.value)}
                required
              />
              <Field
                label='子流路径（可留空）'
                name='sub_path'
                value={subPath}
                onChange={(event) => setSubPath(event.target.value)}
                placeholder='/sub'
              />
            </>
          )}
          <details className='rounded-lg border p-4 md:col-span-2'>
            <summary className='cursor-pointer text-sm font-medium'>
              高级连接设置
            </summary>
            <div className='mt-4 grid gap-4 md:grid-cols-2'>
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
                摄像头操作
                <select
                  aria-label='摄像头操作'
                  value={intent}
                  onChange={(e) => setIntent(e.target.value as typeof intent)}
                  className='rounded-md border bg-background p-2'
                >
                  {revision && (
                    <option value='modify'>修改当前摄像头配置</option>
                  )}
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
                  允许清空用户名（普通编辑留空保留）
                </label>
              </div>
              <label className='grid gap-2 text-sm'>
                密码处理
                <select
                  aria-label='密码处理'
                  value={passwordAction}
                  onChange={(e) => {
                    setPasswordAction(e.target.value as typeof passwordAction)
                    setPassword('')
                  }}
                  className='rounded-md border bg-background p-2'
                >
                  {revision && <option value='keep'>保留当前密码</option>}
                  <option value='replace'>输入新密码</option>
                  <option value='clear'>清空密码</option>
                </select>
              </label>
            </div>
            <p className='mt-4 text-xs text-muted-foreground'>
              更换摄像头仍保留通道编号、权限与历史录像。
            </p>
          </details>
          <div className='flex flex-wrap items-center justify-between gap-3 border-t pt-4 md:col-span-2'>
            <span className='text-xs text-muted-foreground'>
              {method === 'onvif' && !probed
                ? '请先「获取码流」，码流地址由摄像头返回后再保存'
                : '保存不影响正在运行的源；启用才会切换'}
            </span>
            <div className='flex gap-3'>
              {/* The primary action comes first so Enter in any field keeps
                  running the previously verified connect flow. */}
              <Button disabled={pending || (method === 'onvif' && !probed)}>
                {pending ? '正在处理…' : '保存并启用'}
              </Button>
              <Button
                type='submit'
                value='save'
                variant='outline'
                disabled={pending || (method === 'onvif' && !probed)}
              >
                保存
              </Button>
            </div>
          </div>
        </fieldset>
      </form>
    </section>
  )
}
