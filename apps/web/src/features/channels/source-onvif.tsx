import { useState } from 'react'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { Info } from 'lucide-react'
import { Notices, SelectField } from '@/features/foundation/ui'
import { cn } from '@/lib/utils'
import {
  cameraTypeLabel,
  chooseDefaultStreams,
  inferredBrand,
  inferredModel,
  inferCameraType,
  suggestedBrandPreset,
} from './device-meta'

// ONVIF is a manual add method, not a scan: the operator names the camera and
// this control asks it for its own stream configuration. Nothing is stored —
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
  /** The probe result, owned by the caller so the side panel can show it. */
  probe: Schema<'OnvifProbe'> | null
  onProbe: (value: Schema<'OnvifProbe'> | null) => void
  /** Applies the chosen profiles: the caller owns the paths they fill. */
  onChoose: (main: Schema<'OnvifStream'>, sub: Schema<'OnvifStream'> | null) => void
}

/** Quick-pick ports: the three values ONVIF cameras actually ship with. */
const QUICK_PORTS = ['80', '8000', '8899']

function streamLabel(stream: Schema<'OnvifStream'>) {
  const size =
    stream.width > 0 && stream.height > 0
      ? `${stream.width}×${stream.height}`
      : '未知分辨率'
  const encoding = stream.encoding ? ` · ${stream.encoding}` : ''
  return `${stream.name || stream.token} · ${size}${encoding}`
}

/**
 * The probe, as one inline action bar rather than a panel of its own.
 *
 * It used to be a bordered dark box in the middle of the form, which did two
 * things wrong. It read as a section of the page when it is really a step of
 * one field group ("give me the address, then ask the camera"); and it pushed
 * the credential and device fields below it out of view, so the operator could
 * not see the username they were about to send. The controls now sit in the
 * connection row — address, port, quick picks, action — and only the result is
 * rendered elsewhere (the side panel), because a stream list is not a form
 * field.
 */
export function OnvifProbeBar({
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
  probe,
  onProbe,
  onChoose,
}: Props) {
  const [pending, setPending] = useState(false)
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')

  async function request() {
    if (pending || busy) return
    setPending(true)
    setError('')
    setNotice('')
    onProbe(null)
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
      onProbe(value)
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
      // Which profile is the main stream is decided by the URI, then by the
      // device's own order — never by resolution, because a device may report
      // no size for the main profile and that made the two streams swap.
      const { main, sub } = chooseDefaultStreams(value.streams)
      if (!main) {
        setError('设备未提供可用的码流，请检查摄像头配置')
        return
      }
      onChoose(main, sub)
      setNotice(
        value.streams.length > 1
          ? '已读取码流配置，可在右侧核对后再保存'
          : '设备只提供了一个码流，已填入主码流'
      )
    } catch (e) {
      setError(e instanceof ApiError ? e.message : '探测失败，请稍后重试')
    } finally {
      setPending(false)
    }
  }

  return (
    <div className='grid min-w-0 flex-1 gap-1.5'>
      {/* One line: port, its common values, and the action that uses them. The
          address is the field to their left, so "fill IP/port → probe" reads
          left to right in the order it is performed. */}
      <div className='flex flex-wrap items-end gap-2'>
        <div className='grid gap-1'>
          <span className='text-[10px] leading-none text-muted-foreground'>
            ONVIF 端口
          </span>
          <Input
            aria-label='ONVIF 端口'
            className='h-9 w-28 font-mono'
          type='number'
          min={1}
          max={65535}
            placeholder='80'
            value={port}
            onChange={(event) => onPortChange(event.target.value)}
          />
        </div>
        <span className='flex items-center gap-1 pb-1.5'>
          {QUICK_PORTS.map((value) => (
            <button
              key={value}
              type='button'
              aria-pressed={port === value}
              className={cn(
                'h-7 rounded border px-2 font-mono text-[11px] transition-colors',
                port === value
                  ? 'border-foreground/30 bg-foreground/10 text-foreground'
                  : 'border-border text-muted-foreground hover:bg-accent hover:text-foreground'
              )}
              onClick={() => onPortChange(value)}
            >
              {value}
            </button>
          ))}
        </span>
        <Button
          type='button'
          variant='outline'
          size='sm'
          className='h-9'
          disabled={pending || busy || ip.trim() === ''}
          title={ip.trim() === '' ? '请先填写左侧的 IP 地址' : undefined}
          onClick={() => void request()}
        >
          {pending ? '探测中…' : '探测设备与码流'}
        </Button>
      </div>
      {/* The saved-password note is a sub-line of the port field, not a banner:
          it explains what the probe will send, which is a detail of this one
          action. */}
      {savedPassword && (
        <p className='flex w-full flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground'>
          探测使用已保存的密码
          <button
            type='button'
            className='rounded border px-1.5 py-0.5 hover:bg-accent hover:text-foreground'
            onClick={onPasswordNeeded}
          >
            改用新密码
          </button>
        </p>
      )}
      {(error || notice) && (
        <div className='w-full text-[11px]'>
          <Notices error={error} notice={notice} />
        </div>
      )}
      {probe && (
        <p className='flex w-full flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground'>
          <span className='text-foreground'>
            {[probe.device.manufacturer, probe.device.model]
              .filter(Boolean)
              .join(' ') || '未报告品牌与型号'}
          </span>
          {probe.device.firmware_version && (
            <span>固件 {probe.device.firmware_version}</span>
          )}
          <span>
            {probe.ptz
              ? `已按 PTZ 推断为${cameraTypeLabel('ptz')}`
              : '未报告 PTZ，类型需手选'}
          </span>
        </p>
      )}
    </div>
  )
}

/**
 * The streams the device reported, as the caller's side panel renders them.
 *
 * It lives here rather than in the form because a probe result is evidence to
 * compare, not a field to fill: the operator checks the returned URIs against
 * what they expected, and the two selects decide which one becomes main.
 */
export function OnvifStreamList({
  probe,
  mainToken,
  subToken,
  onChoose,
}: {
  probe: Schema<'OnvifProbe'>
  mainToken: string
  subToken: string
  onChoose: (main: Schema<'OnvifStream'>, sub: Schema<'OnvifStream'> | null) => void
}) {
  const options = probe.streams.map((stream) => ({
    value: stream.token,
    label: streamLabel(stream),
  }))
  const find = (token: string) =>
    probe.streams.find((stream) => stream.token === token) ?? null
  return (
    <div className='grid gap-3'>
      <SelectField
        label='主码流'
        className='text-xs'
        value={mainToken}
        onValueChange={(next) => {
          const main = find(next)
          if (main) onChoose(main, find(subToken))
        }}
        options={options}
      />
      <SelectField
        label='子码流（可留空）'
        className='text-xs'
        value={subToken}
        onValueChange={(next) => {
          const main = find(mainToken)
          if (main) onChoose(main, find(next))
        }}
        options={[{ value: '', label: '不使用子码流' }, ...options]}
      />
    </div>
  )
}

/** A label with a hover-only explanation, so the form stays free of grey prose. */
export function InfoHint({ text }: { text: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          tabIndex={0}
          role='img'
          aria-label={text}
          className='inline-grid size-3.5 cursor-help place-items-center rounded-full border border-border text-muted-foreground/70 hover:text-foreground'
        >
          <Info aria-hidden className='size-2.5' />
        </span>
      </TooltipTrigger>
      <TooltipContent className='max-w-64 text-xs'>{text}</TooltipContent>
    </Tooltip>
  )
}
