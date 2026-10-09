import { useState } from 'react'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Notices } from '@/features/foundation/ui'

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

  const options = probe
    ? probe.streams.map((stream) => (
        <option key={stream.token} value={stream.token}>
          {streamLabel(stream)}
        </option>
      ))
    : null
  return (
    <section className='grid gap-3 rounded-lg border p-4'>
      <p className='text-sm text-muted-foreground'>
        ONVIF 方式只填写地址与凭据，码流地址由摄像头返回，不用手填路径。
        探测只读取配置，不保存、不切换当前源。
      </p>
      <div className='flex flex-wrap items-end gap-3'>
        <label className='grid gap-2 text-sm'>
          ONVIF 端口
          <input
            aria-label='ONVIF 端口'
            className='rounded-md border bg-background p-2'
            type='number'
            min={1}
            max={65535}
            placeholder='默认 80，常见 8000'
            value={port}
            onChange={(event) => onPortChange(event.target.value)}
          />
        </label>
        <Button
          type='button'
          variant='outline'
          disabled={pending || busy || ip.trim() === ''}
          onClick={() => void request()}
        >
          {pending ? '获取中…' : '获取码流'}
        </Button>
        {ip.trim() === '' && (
          <span className='pb-2 text-xs text-muted-foreground'>
            请先填写 IP 地址
          </span>
        )}
      </div>
      {savedPassword && (
        <p className='text-xs text-amber-600'>
          已保存的密码不会用于探测；摄像头需要认证时，请把「密码处理」改为「输入新密码」并填写。
        </p>
      )}
      <Notices error={error} notice={notice} />
      {probe && (
        <div className='grid gap-3 rounded-lg border p-3 text-sm'>
          <p>
            设备：
            {[probe.device.manufacturer, probe.device.model]
              .filter(Boolean)
              .join(' ') || '未报告型号'}
            {probe.device.firmware_version
              ? ` · 固件 ${probe.device.firmware_version}`
              : ''}
          </p>
          <p className='text-xs text-muted-foreground'>
            {probe.ptz
              ? '设备报告支持 PTZ，方向与变焦控制后续开放'
              : '设备未报告 PTZ 支持'}
          </p>
          <label className='grid gap-2 text-sm'>
            主码流
            <select
              aria-label='主码流'
              className='rounded-md border bg-background p-2'
              value={mainToken}
              onChange={(event) =>
                choose(probe, event.target.value, subToken)
              }
            >
              {options}
            </select>
          </label>
          <label className='grid gap-2 text-sm'>
            子码流（可留空）
            <select
              aria-label='子码流'
              className='rounded-md border bg-background p-2'
              value={subToken}
              onChange={(event) =>
                choose(probe, mainToken, event.target.value)
              }
            >
              <option value=''>不使用子码流</option>
              {options}
            </select>
          </label>
        </div>
      )}
    </section>
  )
}
