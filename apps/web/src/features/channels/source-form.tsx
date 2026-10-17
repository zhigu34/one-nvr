import { useEffect, useRef, useState, type ReactNode } from 'react'
import { apiRequest, jsonRequest, newRequestKey } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Field,
  SelectField,
  CheckboxField,
  Notices,
} from '@/features/foundation/ui'
import { connectionError, type SaveConnection } from './source-connect'
import { SourceOnvifPanel } from './source-onvif'
import {
  BRAND_PRESETS,
  CAMERA_TYPES,
  pathTemplate,
  type CameraType,
} from './device-meta'

type Props = {
  channel: { id: string; version: number }
  revision: Schema<'SourceRevision'> | null
  history: Schema<'SourceRevision'>[]
  // The test panel renders beside the fields it proves, so the results and the
  // button that produces them are never a scroll apart.
  testPanel?: ReactNode
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
  onSaved,
}: Props) {
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
  const [pathsOpen, setPathsOpen] = useState(true)
  // Device metadata. `recognised` records which fields the probe filled, so the
  // form can mark them as device-reported instead of pretending the operator
  // typed them — and so editing one clears its badge.
  const [brand, setBrand] = useState(revision?.config.brand || '')
  const [model, setModel] = useState(revision?.config.model || '')
  const [cameraType, setCameraType] = useState<CameraType>(
    (revision?.config.camera_type as CameraType) || ''
  )
  const [recognised, setRecognised] = useState<Record<string, boolean>>({})
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
    setPathsOpen(true)
    setBrand(revision?.config.brand || '')
    setModel(revision?.config.model || '')
    setCameraType((revision?.config.camera_type as CameraType) || '')
    setRecognised({})
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
    // What the device returned is worth showing, so the row that holds the
    // filled paths opens instead of hiding them behind a summary line.
    setPathsOpen(true)
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
  const template = pathTemplate(brand)
  const blocked = method === 'onvif' && !probed
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
          // Empty is a valid value: it means "not recorded", and sending the
          // field anyway keeps clearing it expressible.
          config.brand = brand.trim()
          config.model = model.trim()
          config.camera_type = cameraType
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
          <div className='grid gap-5 lg:grid-cols-[minmax(0,1fr)_240px]'>
            <div className='grid content-start gap-4'>
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
                    onClick={() => {
                      setMethod('rtsp')
                      // RTSP means typing the paths, so they are shown; ONVIF
                      // receives them from the device and only needs a summary.
                      setPathsOpen(true)
                    }}
                  >
                    RTSP 手动
                  </Button>
                  <Button
                    type='button'
                    size='sm'
                    variant={method === 'onvif' ? 'secondary' : 'ghost'}
                    aria-pressed={method === 'onvif'}
                    onClick={() => {
                      setMethod('onvif')
                      setPathsOpen(false)
                    }}
                  >
                    ONVIF 手动
                  </Button>
                </div>
                {/* One sentence only: the ONVIF panel repeats the rest where
                    the probe actually happens, and two copies read as two
                    different instructions. */}
                {method === 'rtsp' && (
                  <p className='text-xs text-muted-foreground'>
                    手动填写 RTSP 端口与主/子流路径。
                  </p>
                )}
              </div>
              <div className='grid gap-4 sm:grid-cols-[minmax(0,1fr)_110px]'>
                <div className='grid gap-2'>
                  <label
                    htmlFor='source-ip'
                    className='text-sm font-medium leading-none'
                  >
                    IP 地址
                  </label>
                  <div className='flex'>
                    {/* ONVIF is HTTP/SOAP, not RTSP; showing rtsp:// in that
                        mode described a protocol the probe never uses. */}
                    <span className='inline-flex items-center rounded-l-md border border-r-0 bg-muted/50 px-2 font-mono text-xs text-muted-foreground'>
                      {method === 'onvif' ? 'IP 地址' : 'rtsp://'}
                    </span>
                    <input
                      id='source-ip'
                      name='ip'
                      required
                      placeholder='192.168.33.20'
                      value={ip}
                      onChange={(event) => setIP(event.target.value)}
                      className='flex h-9 w-full min-w-0 rounded-r-md border border-input bg-transparent px-3 py-1 text-base shadow-xs outline-none md:text-sm dark:bg-input/30'
                    />
                  </div>
                  <p className='text-[11px] text-muted-foreground'>
                    {method === 'onvif'
                      ? '地址与凭据会用于探测，不保存凭据。'
                      : '填写摄像头地址，路径在下方「码流路径」。'}
                  </p>
                </div>
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
              </div>
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
                {/* Choosing what happens to the stored password is an ordinary
                    step of editing a camera, so it stays beside the credential
                    fields instead of hiding behind a collapsible section. */}
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
              {method === 'onvif' && (
                <SourceOnvifPanel
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
                    setRecognised({
                      brand: !!device.brand,
                      model: !!device.model,
                      cameraType: !!device.cameraType,
                    })
                  }}
                  onPasswordNeeded={() => {
                    // Typing a password is the same intent as choosing "replace";
                    // making the operator pick it from a dropdown afterwards is
                    // one step too many.
                    setPasswordAction('replace')
                  }}
                />
              )}
              <div className='grid gap-4 rounded-lg border p-3 sm:grid-cols-3'>
                <div className='grid gap-2'>
                  <label htmlFor='source-brand' className='text-sm font-medium'>
                    摄像头品牌
                    {recognised.brand && <RecognisedBadge />}
                  </label>
                  {/* The label above carries the "已自动识别" badge, so this
                      input is not wrapped in <Field>: that component renders its
                      own label and would produce a second, empty one. */}
                  <Input
                    id='source-brand'
                    name='brand'
                    list='brand-presets'
                    maxLength={64}
                    placeholder='如 海康威视'
                    value={brand}
                    onChange={(event) => {
                      setBrand(event.target.value)
                      // Editing a device-reported value makes it the operator's
                      // own, so the badge must not keep claiming the device said it.
                      setRecognised((current) => ({ ...current, brand: false }))
                    }}
                  />
                  <datalist id='brand-presets'>
                    {BRAND_PRESETS.map((value) => (
                      <option key={value} value={value} />
                    ))}
                  </datalist>
                </div>
                <div className='grid gap-2'>
                  <label htmlFor='source-model' className='text-sm font-medium'>
                    设备型号
                    {recognised.model && <RecognisedBadge />}
                  </label>
                  <Input
                    id='source-model'
                    name='model'
                    maxLength={128}
                    placeholder='选填，或通过 ONVIF 探测自动回填'
                    value={model}
                    onChange={(event) => {
                      setModel(event.target.value)
                      setRecognised((current) => ({ ...current, model: false }))
                    }}
                  />
                </div>
                <div className='grid gap-2'>
                  <label className='text-sm font-medium'>
                    摄像头类型
                    {recognised.cameraType && <RecognisedBadge />}
                  </label>
                  <SelectField
                    ariaLabel='摄像头类型'
                    value={cameraType}
                    onValueChange={(value) => {
                      setCameraType(value as CameraType)
                      setRecognised((current) => ({
                        ...current,
                        cameraType: false,
                      }))
                    }}
                    options={[
                      { value: '', label: '未记录' },
                      ...CAMERA_TYPES.map((entry) => ({
                        value: entry.value,
                        label: entry.label,
                      })),
                    ]}
                  />
                </div>
                <p className='text-xs text-muted-foreground sm:col-span-3'>
                  设备信息只用于展示与检索，不影响取流；ONVIF 探测会读取品牌与型号，并据 PTZ 能力推断类型，可手动修改。
                </p>
              </div>
              {(method === 'rtsp' || probed) && (
                <details
                  className='rounded-lg border'
                  open={pathsOpen}
                  onToggle={(event) => setPathsOpen(event.currentTarget.open)}
                >
                  <summary className='flex cursor-pointer items-center justify-between gap-3 px-3 py-2.5 text-sm font-medium'>
                    <span>码流路径</span>
                    {/* The paths themselves are one line below in the fields;
                        repeating them here just competed with the template
                        action. */}
                    {method === 'rtsp' && template ? (
                      <span
                        role='button'
                        tabIndex={0}
                        title={template.hint}
                        className='rounded-md border px-2 py-0.5 text-[11px] font-normal text-muted-foreground hover:bg-accent hover:text-foreground'
                        onClick={(event) => {
                          // The button lives inside <summary>: without this the
                          // click would also toggle the disclosure.
                          event.preventDefault()
                          event.stopPropagation()
                          setMainPath(template.main)
                          setSubPath(template.sub)
                        }}
                        onKeyDown={(event) => {
                          if (event.key !== 'Enter' && event.key !== ' ') return
                          event.preventDefault()
                          event.stopPropagation()
                          setMainPath(template.main)
                          setSubPath(template.sub)
                        }}
                      >
                        填入{template.hint}
                      </span>
                    ) : (
                      <span className='text-[11px] font-normal text-muted-foreground'>
                        {method === 'onvif' ? '由摄像头返回' : ''}
                      </span>
                    )}
                  </summary>
                  <div className='grid gap-4 border-t p-3 sm:grid-cols-2'>
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
                </details>
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
            {testPanel ? (
              <div className='lg:sticky lg:top-4'>{testPanel}</div>
            ) : (
              <div />
            )}
          </div>
          <div className='flex flex-wrap items-center justify-between gap-3 border-t pt-4'>
            <p className='text-xs text-muted-foreground'>
              {blocked
                ? '请先「获取码流」，码流地址由摄像头返回后再保存'
                : '保存只记录配置，不影响正在取流；启用会先测试、再切换，约 30 秒内生效。'}
            </p>
            <div className='flex items-center gap-2'>
              {/* Tree order decides which button Enter submits, so the primary
                  action stays first in the DOM and `order` puts the quieter one
                  on the left where it reads as secondary. */}
              <Button className='order-2' disabled={pending || blocked}>
                {pending ? '正在处理…' : '保存并启用'}
              </Button>
              <Button
                type='submit'
                value='save'
                variant='ghost'
                className='order-1'
                disabled={pending || blocked}
              >
                仅保存
              </Button>
            </div>
          </div>
        </fieldset>
      </form>
    </section>
  )
}

/**
 * RecognisedBadge marks a field the ONVIF probe filled.
 *
 * The operator needs to know which values came from the device and which they
 * typed: a device-reported model is evidence about the hardware, while a typed
 * one is a note. Editing the field clears the badge, because the value is then
 * the operator's own.
 */
function RecognisedBadge() {
  return (
    <span className='ml-1.5 rounded border border-emerald-500/30 bg-emerald-500/10 px-1 py-px align-middle text-[10px] font-normal text-emerald-500'>
      已自动识别
    </span>
  )
}
