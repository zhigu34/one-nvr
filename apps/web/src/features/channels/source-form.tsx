import { useEffect, useRef, useState, type ReactNode } from 'react'
import { apiRequest, jsonRequest, newRequestKey } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import {
  Field,
  SelectField,
  CheckboxField,
  Notices,
} from '@/features/foundation/ui'
import { connectionError, type SaveConnection } from './source-connect'
import { InfoHint, OnvifProbeBar } from './source-onvif'
import { pathTemplate, type CameraType } from './device-meta'

type Props = {
  channel: {
    id: string
    version: number
    // Read-only here: the brand selects the RTSP path template. It is edited in
    // the channel header, because writing it belongs on the channel — a change
    // to it must not produce a source revision.
    brand?: string
  }
  revision: Schema<'SourceRevision'> | null
  history: Schema<'SourceRevision'>[]
  // The side panel renders beside the fields it proves, so the results and the
  // button that produces them are never a scroll apart.
  testPanel?: ReactNode
  // The probe's stream list, rendered into the same sticky side panel: a list of
  // returned URIs is evidence to compare, not a form field.
  streamsPanel?: (
    probe: Schema<'OnvifProbe'> | null,
    tokens: { main: string; sub: string },
    choose: (
      main: Schema<'OnvifStream'>,
      sub: Schema<'OnvifStream'> | null
    ) => void
  ) => ReactNode
  onSaved: (
    value: Schema<'SourceRevision'>,
    signal: AbortSignal,
    context: SaveConnection
  ) => void | string | Promise<void | string>
}
export function SourceForm(props: Props) {
  return <SourceEditor key={props.channel.id} {...props} />
}
function SourceEditor({
  channel,
  revision,
  history,
  testPanel,
  streamsPanel,
  onSaved,
}: Props) {
  const [passwordAction, setPasswordAction] = useState<
    Schema<'CredentialInput'>['password_action']
  >(revision ? 'keep' : 'replace')
  const [intent, setIntent] = useState<Schema<'DraftInput'>['identity_intent']>(
    revision ? 'modify' : 'replace'
  )
  // ONVIF is a manual add method now: the operator names the camera and the
  // probe asks it for its own stream configuration. The connection fields are
  // controlled so a probe can fill them, while every write still happens through
  // the same save / save-and-enable submission below.
  const [method, setMethod] = useState<'rtsp' | 'onvif'>('rtsp')
  const [ip, setIP] = useState(revision?.config.ip || '')
  const [rtspPort, setRTSPPort] = useState(
    String(revision?.config.rtsp_port || 554)
  )
  // Seeded from the channel (the storage of record) and overwritten by a probe.
  // It is not edited here — see the header editor — but both the RTSP path
  // template and the identity patch need its current value.
  const [brand, setBrand] = useState(channel.brand || revision?.config.brand || '')
  const [mainPath, setMainPath] = useState(
    revision?.config.main_path || '/main'
  )
  const [subPath, setSubPath] = useState(revision?.config.sub_path || '')
  const [onvifPort, setONVIFPort] = useState(
    revision?.config.onvif_port ? String(revision.config.onvif_port) : ''
  )
  const [probed, setProbed] = useState(false)
  // The probe result is held here rather than inside the probe bar: the side
  // panel shows the returned streams, and a child's local state cannot reach a
  // sibling.
  const [probe, setProbe] = useState<Schema<'OnvifProbe'> | null>(null)
  const [mainToken, setMainToken] = useState('')
  const [subToken, setSubToken] = useState('')
  // Device metadata. `recognised` records which fields the probe filled, so the
  // form can mark them as device-reported instead of pretending the operator
  // typed them — and so editing one clears its badge.
  const [model, setModel] = useState(revision?.config.model || '')
  const [cameraType, setCameraType] = useState<CameraType>(
    (revision?.config.camera_type as CameraType) || ''
  )
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
    setProbe(null)
    setMainToken('')
    setSubToken('')
    setModel(revision?.config.model || '')
    setBrand(channel.brand || revision?.config.brand || '')
    setCameraType((revision?.config.camera_type as CameraType) || '')
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
  // ONVIF has nothing to submit until the camera answered, so both actions stay
  // unavailable before a probe fills the paths.
  // Only offered in the manual-RTSP mode: in ONVIF mode the device supplies the
  // paths, and a brand template would overwrite what it just returned.
  const template = pathTemplate(channel.brand || revision?.config.brand || '')
  const blocked = method === 'onvif' && !probed
  /**
   * The identity fields that differ from what the channel already holds.
   *
   * Returns null when nothing changed, so a plain stream edit issues no second
   * request. The probe fills these freely; the operator may overwrite them; both
   * end up on the channel because that is where "what this camera is" lives.
   */
  function identityPatch() {
    const next = {
      brand: brand.trim(),
      model: model.trim(),
      camera_type: cameraType,
    }
    const current = {
      brand: (revision?.config.brand || '').trim(),
      model: (revision?.config.model || '').trim(),
      camera_type: revision?.config.camera_type || '',
    }
    const changed = (Object.keys(next) as (keyof typeof next)[]).some(
      (key) => next[key] !== current[key]
    )
    return changed ? next : null
  }
  return (
    <section className='grid gap-4'>
      <Notices error={error} notice={notice} />
      <form
        key={revision?.id || 'empty'}
        ref={form}
        className='grid gap-5'
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
          // The probe's answer is applied to the CHANNEL, not baked into the
          // revision. Identity is not part of what a revision describes, so it
          // is written by its own PATCH after the revision is saved — see
          // `identityPatch` below. Sending it here would tie a brand correction
          // to a re-test and a stream switch.
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
              // Identity is written after the revision, in its own request with
              // its own version check: it must not ride on the revision, and it
              // must not block the save if it fails.
              const identity = identityPatch()
              if (identity) {
                try {
                  await apiRequest(`/api/v1/channels/${channel.id}`, {
                    ...jsonRequest('PATCH', identity, channel.version + 1),
                    signal: abort.signal,
                  })
                } catch {
                  // Reported as a notice rather than an error: the stream
                  // configuration was saved, and only the description of the
                  // hardware failed to.
                  if (!abort.signal.aborted) {
                    setNotice('摄像头型号未更新（其余配置已保存）')
                  }
                }
              }
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
          {/* 8:4. The form is a sequence of short field groups; the side column
              holds the evidence and the decisions about it (test, streams,
              save), which is why it is sticky: the operator keeps the verdict
              in view while editing the address it refers to. */}
          <div className='grid items-start gap-5 lg:grid-cols-12'>
            <div className='grid content-start gap-5 lg:col-span-9'>
              <div className='grid gap-2'>
                <span className='text-sm font-medium'>添加方式</span>
                <div
                  role='group'
                  aria-label='添加方式'
                  className='inline-flex w-fit rounded-lg border bg-muted/40 p-0.5'
                >
                  <Button
                    type='button'
                    size='sm'
                    variant={method === 'rtsp' ? 'secondary' : 'ghost'}
                    aria-pressed={method === 'rtsp'}
                    onClick={() => setMethod('rtsp')}
                  >
                    RTSP 手动
                  </Button>
                  <Button
                    type='button'
                    size='sm'
                    variant={method === 'onvif' ? 'secondary' : 'ghost'}
                    aria-pressed={method === 'onvif'}
                    onClick={() => setMethod('onvif')}
                  >
                    ONVIF 手动
                  </Button>
                </div>
              </div>

              {/* Network: address, port and — in ONVIF mode — the probe, all on
                  one baseline. The explanation that used to sit under this row
                  ("地址与凭据会用于探测…") is on the label's info icon now. */}
              {/* Network, on a 9/3 grid with both captions on their own inputs.
                  It was a flex row before, which put "IP 地址" at the far left and
                  "RTSP 端口" at the far right with their inputs at different
                  heights — the two halves of one address read as two unrelated
                  fields. */}
              <div className='grid grid-cols-12 items-end gap-3'>
                <div className='col-span-9 grid gap-1.5'>
                  <span className='flex items-center gap-1.5 text-xs text-muted-foreground'>
                    <label htmlFor='source-ip'>IP 地址</label>
                    <InfoHint
                      text={
                        method === 'onvif'
                          ? '地址与凭据只用于探测，不会保存探测凭据。'
                          : '摄像头地址；端口与路径在右侧。'
                      }
                    />
                  </span>
                  <div
                    className={cn(
                      'flex overflow-hidden rounded-md border border-input bg-transparent shadow-xs transition-colors focus-within:border-ring dark:bg-input/30'
                    )}
                  >
                    {/* The prefix belongs to the field in RTSP mode only: ONVIF
                        probes over HTTP/SOAP, where it named the wrong protocol. */}
                    {method === 'rtsp' && (
                      <span className='inline-flex select-none items-center border-r border-input bg-muted/50 px-3 font-mono text-xs text-muted-foreground'>
                        rtsp://
                      </span>
                    )}
                    <input
                      id='source-ip'
                      name='ip'
                      required
                      placeholder='192.168.66.110'
                      value={ip}
                      onChange={(event) => setIP(event.target.value)}
                      className='h-9 min-w-0 flex-1 bg-transparent px-3 font-mono text-sm outline-none'
                    />
                  </div>
                </div>
                {(method === 'rtsp' || probed) && (
                  <div className='col-span-3 grid gap-1.5'>
                    <label
                      htmlFor='source-rtsp-port'
                      className='text-xs text-muted-foreground'
                    >
                      RTSP 端口
                    </label>
                    <input
                      id='source-rtsp-port'
                      aria-label='RTSP 端口'
                      name='rtsp_port'
                      type='number'
                      required
                      min={1}
                      max={65535}
                      value={rtspPort}
                      onChange={(event) => setRTSPPort(event.target.value)}
                      className='h-9 w-full rounded-md border border-input bg-transparent px-3 font-mono text-sm shadow-xs outline-none dark:bg-input/30'
                    />
                  </div>
                )}
                {method === 'onvif' && (
                  <div className='col-span-9 grid gap-1.5'>
                    <OnvifProbeBar
                      channelId={channel.id}
                      ip={ip}
                      port={onvifPort}
                      onPortChange={setONVIFPort}
                      credentials={enteredCredentials}
                      onStreams={applyStreams}
                      savedPassword={passwordAction === 'keep' && !!revision}
                      busy={pending}
                      onDevice={(device) => {
                        if (device.brand) setBrand(device.brand)
                        if (device.model) setModel(device.model)
                        if (device.cameraType) {
                          setCameraType(device.cameraType as CameraType)
                        }
                      }}
                      onPasswordNeeded={() => setPasswordAction('replace')}
                      probe={probe}
                      onProbe={setProbe}
                      onChoose={(main, sub) => {
                        setMainToken(main.token)
                        setSubToken(sub ? sub.token : '')
                        applyStreams(main, sub)
                      }}
                    />
                  </div>
                )}
              </div>

              {/* Credentials, 2×2. Four fields on two lines instead of four
                  lines: they are read together when a camera rejects a login. */}
              <div className='grid gap-4 sm:grid-cols-2'>
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
                    placeholder={
                      revision ? '留空保留已保存的密码' : '摄像头密码'
                    }
                    value={password}
                    onChange={(event) => {
                      setPassword(event.target.value)
                      // Typing is an unambiguous statement of intent, so the
                      // action follows it instead of making the operator pick
                      // "输入新密码" from a dropdown afterwards.
                      setPasswordAction(
                        revision && !event.target.value ? 'keep' : 'replace'
                      )
                    }}
                    onFocus={() => {
                      // Focusing to type is the same intent; without this the
                      // field stays disabled-looking next to a "keep" selector
                      // and the first keystroke lands before the switch.
                      if (revision && passwordAction === 'keep') {
                        setPasswordAction('replace')
                      }
                    }}
                  />
                )}
                <SelectField
                  label='密码处理'
                  value={passwordAction}
                  onValueChange={(next) => {
                    setPasswordAction(next as typeof passwordAction)
                    setPassword('')
                  }}
                  options={[
                    ...(revision
                      ? [{ value: 'keep', label: '保留当前密码' }]
                      : []),
                    { value: 'replace', label: '输入新密码' },
                    { value: 'clear', label: '清空密码' },
                  ]}
                />
                <SelectField
                  label='传输方式'
                  name='transport'
                  defaultValue={revision?.config.transport || 'tcp'}
                  options={[
                    { value: 'tcp', label: 'TCP' },
                    { value: 'udp', label: 'UDP' },
                  ]}
                />
              </div>

              {(method === 'rtsp' || probed) && (
                <div className='grid gap-2 border-t border-border/60 pt-4'>
                  <div className='flex flex-wrap items-center justify-between gap-2'>
                    <span className='text-sm font-medium'>码流路径</span>
                    {method === 'rtsp' && template ? (
                      <button
                        type='button'
                        title={template.hint}
                        className='rounded-md border px-2 py-0.5 text-[11px] text-muted-foreground hover:bg-accent hover:text-foreground'
                        onClick={() => {
                          setMainPath(template.main)
                          setSubPath(template.sub)
                        }}
                      >
                        填入{template.hint}
                      </button>
                    ) : (
                      <span className='text-[11px] text-muted-foreground'>
                        {method === 'onvif' ? '由摄像头返回' : ''}
                      </span>
                    )}
                  </div>
                  <div className='grid gap-4 sm:grid-cols-2'>
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
                  </div>
                </div>
              )}
              <details className='rounded-lg border'>
                <summary className='cursor-pointer px-3 py-2.5 text-sm font-medium'>
                  高级设置
                </summary>
                <div className='grid gap-4 border-t p-3 sm:grid-cols-2'>
                  <SelectField
                    label='摄像头操作'
                    value={intent}
                    onValueChange={(next) => setIntent(next as typeof intent)}
                    options={[
                      ...(revision
                        ? [{ value: 'modify', label: '修改当前摄像头配置' }]
                        : []),
                      { value: 'replace', label: '更换摄像头' },
                      ...(identities.length > 0
                        ? [{ value: 'history', label: '恢复历史摄像头身份' }]
                        : []),
                    ]}
                  />
                  {intent === 'history' && (
                    <SelectField
                      label='历史摄像头'
                      name='history_source_id'
                      defaultValue={identities[0]?.source_id}
                      options={identities.map((value) => ({
                        value: value.source_id,
                        label: `身份 ${value.source_id.slice(0, 8)} · 修订 ${value.number}`,
                      }))}
                    />
                  )}
                  <div className='grid gap-2'>
                    <CheckboxField
                      label='允许清空用户名（普通编辑留空保留）'
                      name='update_username'
                      defaultChecked={!revision}
                    />
                  </div>
                </div>
                <p className='border-t px-3 py-2.5 text-xs text-muted-foreground'>
                  更换摄像头仍保留通道编号、权限与历史录像。
                </p>
              </details>
            </div>

            {/* The side column: test evidence, the returned streams, and the
                two actions that conclude the edit. Sticky, because all three
                answer "is this right" while the operator is still typing. */}
            <div className='grid gap-4 lg:sticky lg:top-4 lg:col-span-3'>
              {testPanel}
              {streamsPanel &&
                streamsPanel(
                  probe,
                  { main: mainToken, sub: subToken },
                  (main, sub) => {
                    setMainToken(main.token)
                    setSubToken(sub ? sub.token : '')
                    applyStreams(main, sub)
                  }
                )}
              <div className='grid gap-2 rounded-lg border bg-card p-3'>
                <p className='text-xs text-muted-foreground'>
                  {blocked
                    ? '先「探测设备与码流」，码流地址由摄像头返回'
                    : '保存只记录配置；启用会先测试、再切换，约 30 秒生效'}
                </p>
                {/* Tree order decides which button Enter submits, so the primary
                    action stays first in the DOM. */}
                <Button disabled={pending || blocked}>
                  {pending ? '正在处理…' : '保存并启用'}
                </Button>
                <Button
                  type='submit'
                  value='save'
                  variant='outline'
                  disabled={pending || blocked}
                >
                  仅保存
                </Button>
              </div>
            </div>
          </div>
        </fieldset>
      </form>
    </section>
  )
}

