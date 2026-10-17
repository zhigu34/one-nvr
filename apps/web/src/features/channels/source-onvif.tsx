import { useState } from 'react'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Notices, SelectField } from '@/features/foundation/ui'
import {
  cameraTypeLabel,
  inferredBrand,
  inferredModel,
  inferCameraType,
  suggestedBrandPreset,
} from './device-meta'

// ONVIF is a manual add method, not a scan: the operator names the camera and
// this panel asks it for its own stream configuration. Nothing is stored here —
// the answer only fills the form, and saving still goes through the normal
// revision, test and apply flow.
type Props = {
  channelId: string
  ip: string
  port: string
  onPortChange: (value: string) => void
  credentials: () => { username: string; password: string }
  onStreams: (
    main: Schema<'OnvifStream'> | null,
    sub: Schema<'OnvifStream'> | null
  ) => void
  // A stored password exists that this form cannot hand to the probe. Saying so
  // is better than letting the probe fail with an authentication error.
  savedPassword: boolean
  busy: boolean
  /** Fills the device fields and reports which ones the device reported. */
  onDevice: (device: { brand: string; model: string; cameraType: string }) => void
  /** Switches the password action to "replace" when the operator starts typing. */
  onPasswordNeeded: () => void
}

function pixels(stream: Schema<'OnvifStream'>) {
  return stream.width * stream.height
}

function streamLabel(stream: Schema<'OnvifStream'>) {
  const size =
    stream.width > 0 && stream.height > 0
      ? `${stream.width}×${stream.height}`
      : '未知分辨率'
  const encoding = stream.encoding ? ` · ${stream.encoding}` : ''
  return `${stream.name || stream.token} · ${size}${encoding}`
}

export function SourceOnvifPanel({
  channelId,
  ip,
  port,
  onPortChange,
  credentials,
  onStreams,
  savedPassword,
  busy,
  onDevice,
  onPasswordNeeded,
}: Props) {
  const [probe, setProbe] = useState<Schema<'OnvifProbe'> | null>(null)
  const [mainToken, setMainToken] = useState('')
  const [subToken, setSubToken] = useState('')
  const [pending, setPending] = useState(false)
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')

  function choose(
    value: Schema<'OnvifProbe'>,
    main: string,
    sub: string
  ): void {
    setMainToken(main)
    setSubToken(sub)
    const find = (token: string) =>
      value.streams.find((stream) => stream.token === token) || null
    onStreams(find(main), find(sub))
  }

  async function request() {
    if (pending || busy) return
    setPending(true)
    setError('')
    setNotice('')
    setProbe(null)
    onStreams(null, null)
    const { username, password } = credentials()
    try {
      const value = await apiRequest<Schema<'OnvifProbe'>>(
        `/api/v1/channels/${channelId}/onvif/probe`,
        jsonRequest('POST', {
          ip,
          ...(port.trim() === '' ? {} : { onvif_port: Number(port) }),
          username,
          password,
        })
      )
      setProbe(value)
      // The device reports its own manufacturer and model; the type can only be
      // inferred from the PTZ node. All three land in the form as suggestions the
      // operator may overwrite.
      const preset = suggestedBrandPreset(value.device.manufacturer)
      onDevice({
        brand: preset || inferredBrand(value.device.manufacturer),
        model: inferredModel(value.device.model),
        cameraType: inferCameraType(value.ptz),
      })
      // Record the port the probe actually used (the server resolves an empty
      // field to the device default): saving must not silently drop the ONVIF
      // identity just because the operator trusted the default.
      onPortChange(String(value.onvif_port))
      // Default to the largest stream as the main one and the next distinct
      // stream as the sub stream, so the common case needs no editing.
      const ranked = [...value.streams].sort((a, b) => pixels(b) - pixels(a))
      const main = ranked[0]
      if (!main) {
        setError('设备未提供可用的码流，请检查摄像头配置')
        return
      }
      const sub = ranked.find((stream) => stream.token !== main.token)
      choose(value, main.token, sub ? sub.token : '')
      setNotice(
        value.streams.length > 1
          ? '已读取码流配置，可按需调整主/子码流后再保存'
          : '设备只提供了一个码流，已填入主码流'
      )
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '探测失败，请稍后重试')
    } finally {
      setPending(false)
    }
  }

  const streamOptions = probe
    ? probe.streams.map((stream) => ({
        value: stream.token,
        label: streamLabel(stream),
      }))
    : []
  return (
    <section className='grid gap-3 rounded-lg border p-4'>
      <p className='text-xs text-muted-foreground'>
        探测只读取配置，不保存、不切换当前源。
      </p>
      <div className='flex flex-wrap items-end gap-3'>
        <label className='grid gap-2 text-sm'>
          ONVIF 端口
          <input
            aria-label='ONVIF 端口'
            /* Wide enough for the whole placeholder: at the previous width
               "默认 80，常见 8000" was clipped to "默认 80". */
            className='w-64 rounded-md border bg-background p-2'
            type='number'
            min={1}
            max={65535}
            placeholder='默认 80，常见 8000 / 8899'
            value={port}
            onChange={(event) => onPortChange(event.target.value)}
          />
          <span className='flex gap-1 text-[11px] text-muted-foreground'>
            常见端口：
            {['80', '8000', '8899'].map((value) => (
              <button
                key={value}
                type='button'
                className='rounded border px-1 hover:bg-accent'
                onClick={() => onPortChange(value)}
              >
                {value}
              </button>
            ))}
          </span>
        </label>
        <Button
          type='button'
          variant='outline'
          disabled={pending || busy || ip.trim() === ''}
          onClick={() => void request()}
        >
          {pending ? '探测中…' : '探测设备与码流'}
        </Button>
        {ip.trim() === '' && (
          <span className='pb-2 text-xs text-muted-foreground'>
            请先填写 IP 地址
          </span>
        )}
      </div>
      {savedPassword && (
        /* An actionable hint instead of an instruction: the operator should not
           have to find a dropdown after being told the probe will fail. */
        <p className='flex flex-wrap items-center gap-2 text-xs text-amber-600'>
          已保存的密码不会用于探测。
          <button
            type='button'
            className='rounded border border-amber-600/40 px-1.5 py-0.5 hover:bg-amber-500/10'
            onClick={onPasswordNeeded}
          >
            改为输入新密码
          </button>
        </p>
      )}
      <Notices error={error} notice={notice} />
      {probe && (
        <div className='grid gap-3 rounded-lg border p-3 text-sm'>
          <div className='grid gap-1'>
            <p className='flex flex-wrap items-center gap-2'>
              <span className='text-muted-foreground'>设备</span>
              {[probe.device.manufacturer, probe.device.model]
                .filter(Boolean)
                .join(' ') || '未报告品牌与型号'}
              <span className='rounded border border-emerald-500/30 bg-emerald-500/10 px-1 py-px text-[10px] text-emerald-500'>
                已自动识别
              </span>
            </p>
            {probe.device.firmware_version && (
              <p className='text-xs text-muted-foreground'>
                固件 {probe.device.firmware_version}
                {probe.device.serial_number
                  ? ` · 序列号 ${probe.device.serial_number}`
                  : ''}
              </p>
            )}
            <p className='text-xs text-muted-foreground'>
              {probe.ptz
                ? `类型已按 PTZ 能力推断为「${cameraTypeLabel('ptz')}」，方向与变焦控制后续开放`
                : '设备未报告 PTZ，类型需手动选择（枪机与半球无法通过 ONVIF 区分）'}
            </p>
          </div>
          <SelectField
            label='主码流'
            className='text-sm'
            value={mainToken}
            onValueChange={(next) => {
              if (probe) choose(probe, next, subToken)
            }}
            options={streamOptions}
          />
          <SelectField
            label='子码流（可留空）'
            className='text-sm'
            value={subToken}
            onValueChange={(next) => {
              if (probe) choose(probe, mainToken, next)
            }}
            options={[
              { value: '', label: '不使用子码流' },
              ...streamOptions,
            ]}
          />
        </div>
      )}
    </section>
  )
}
