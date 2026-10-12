import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { useAPI } from '@/features/foundation/hooks'
import { Empty, Notices, QueryState, SelectField } from '@/features/foundation/ui'

type Config = Schema<'DetectionConfig'>
type Runtime = Schema<'DetectionRuntime'>
type Channel = Schema<'Channel'>

const LABEL_TEXT: Record<string, string> = {
  person: '人', bicycle: '自行车', car: '轿车', motorcycle: '摩托车', bus: '巴士',
  truck: '卡车', cat: '猫', dog: '狗', bird: '鸟', horse: '马', sheep: '羊', cow: '牛', bear: '熊',
}

function errorText(error: unknown): string {
  if (error instanceof ApiError) {
    const texts: Record<string, string> = {
      detection_label_unsupported: '模型不支持所选目标类别',
      detection_version_conflict: '配置已被他人修改，请刷新后重试',
      detection_publish_failed: '发布失败，已保留原配置',
    }
    if (texts[error.code]) return texts[error.code]
    return error.message
  }
  return error instanceof Error ? error.message : '操作失败'
}

/**
 * M3 batch 2 (AI-01/AI-07): per-channel detection settings and the capability
 * card. Publication is an explicit action: editing a row only saves it, and
 * the button at the top renders and reloads Frigate's config. Recording is
 * never touched by any of this.
 */
export function Detection() {
  const client = useQueryClient()
  const channels = useAPI<PageData<Channel>>('/api/v1/channels?limit=100')
  const configs = useAPI<{ items: Config[] }>('/api/v1/detection/configs')
  const runtime = useAPI<Runtime>('/api/v1/detection/runtime')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  const permitted = (channels.data?.items || []).filter((channel) =>
    channel.permissions.includes('configure')
  )
  const configOf = new Map((configs.data?.items || []).map((config) => [config.channel_id ?? '', config]))

  async function run(action: () => Promise<void>, message: string) {
    setPending(true)
    setError('')
    setNotice('')
    try {
      await action()
      setNotice(message)
      await client.invalidateQueries()
    } catch (cause) {
      setError(errorText(cause))
    } finally {
      setPending(false)
    }
  }

  return (
    <div className='space-y-4'>
      <div>
        <h1 className='text-2xl font-semibold tracking-tight'>检测配置</h1>
        <p className='mt-1 text-sm text-muted-foreground'>
          按通道启停智能检测并选择码流、帧率与目标类别。检测从媒体服务器内部取流，只做检测与抓拍，不接管录像；发布只影响检测服务，不中断录像。
        </p>
      </div>
      <QueryState
        pending={channels.isPending || configs.isPending}
        error={channels.error || configs.error}
        retry={() => void channels.refetch()}
      />
      <Notices notice={notice} error={error} />
      {runtime.data && (
        <div className='grid gap-2 rounded-xl border bg-card p-4 text-sm sm:grid-cols-2 lg:grid-cols-4'>
          <div>
            <p className='text-xs text-muted-foreground'>检测服务</p>
            <p>{runtime.data.runtime?.reachable ? '运行中' : '不可达'}</p>
          </div>
          <div>
            <p className='text-xs text-muted-foreground'>版本 / 检测器</p>
            <p>{runtime.data.runtime?.version || '—'} · {runtime.data.runtime?.detector || '—'}</p>
          </div>
          <div>
            <p className='text-xs text-muted-foreground'>推理耗时</p>
            <p>
              {runtime.data.runtime?.inference_ms ? `${runtime.data.runtime.inference_ms.toFixed(1)} ms` : '—'}
              {runtime.data.runtime?.overloaded ? ' · 过载' : ''}
            </p>
          </div>
          <div>
            <p className='text-xs text-muted-foreground'>最近发布</p>
            <p>
              {runtime.data.revision
                ? `${runtime.data.revision.state === 'published' ? '成功' : '失败'} · ${runtime.data.revision.camera_count} 路`
                : '尚未发布'}
            </p>
          </div>
        </div>
      )}
      {(!permitted.length && channels.data) ? (
        <Empty>没有可配置检测的通道，请联系管理员分配通道配置权限。</Empty>
      ) : (
        <>
          <div className='flex items-center gap-2'>
            <Button
              disabled={pending}
              onClick={() =>
                void run(async () => {
                  await apiRequest('/api/v1/detection/publish', { method: 'POST' })
                }, '检测配置已发布')
              }
            >
              发布检测配置
            </Button>
            <span className='text-xs text-muted-foreground'>
              发布将渲染检测服务配置并触发热加载；失败会保留原配置。
            </span>
          </div>
          <div className='space-y-2'>
            {permitted.map((channel) => {
              const config = configOf.get(channel.id ?? '')
              return (
                <DetectionRow
                  key={channel.id}
                  channel={channel}
                  config={config}
                  labels={runtime.data?.supported_labels || []}
                  pending={pending}
                  onSave={(next) =>
                    void run(async () => {
                      await apiRequest(
                        `/api/v1/detection/configs/${channel.id}`,
                        jsonRequest('PUT', { ...next, version: config?.version ?? 0 })
                      )
                    }, '检测配置已保存，发布后生效')
                  }
                />
              )
            })}
          </div>
        </>
      )}
    </div>
  )
}

function DetectionRow({
  channel,
  config,
  labels,
  pending,
  onSave,
}: {
  channel: Channel
  config?: Config
  labels: string[]
  pending: boolean
  onSave: (next: { enabled: boolean; stream: string; fps: number; labels: string[] }) => void
}) {
  const [enabled, setEnabled] = useState(config?.enabled ?? false)
  const [stream, setStream] = useState(config?.stream ?? 'sub')
  const [fps, setFps] = useState(config?.fps ?? 5)
  const [selected, setSelected] = useState<string[]>(config?.labels ?? ['person'])
  const label = `CH${String(channel.channel_no ?? 0).padStart(2, '0')} ${channel.channel_name}`

  return (
    <div className='flex flex-wrap items-center gap-3 rounded-xl border bg-card p-3 text-sm' data-testid='detection-row'>
      <span className='w-40 truncate'>{label}</span>
      <label className='flex items-center gap-2'>
        <Checkbox
          checked={enabled}
          onCheckedChange={(next) => setEnabled(next === true)}
          aria-label={`${label} 启用检测`}
        />
        启用
      </label>
      <SelectField
        label={`${label} 检测码流`}
        value={stream}
        onValueChange={(next) => setStream((next ?? 'sub') as 'main' | 'sub')}
        options={[
          { value: 'sub', label: '子码流' },
          { value: 'main', label: '主码流' },
        ]}
        className='w-28'
      />
      <SelectField
        label={`${label} 检测帧率`}
        value={String(fps)}
        onValueChange={(next) => setFps(Number(next ?? 5))}
        options={[2, 5, 10, 15].map((value) => ({ value: String(value), label: `${value} fps` }))}
        className='w-24'
      />
      <div className='flex flex-wrap items-center gap-2'>
        {labels.map((value) => (
          <label key={value} className='flex items-center gap-1 text-xs'>
            <Checkbox
              checked={selected.includes(value)}
              onCheckedChange={(next) => {
                setSelected((current) =>
                  next === true
                    ? [...new Set([...current, value])]
                    : current.filter((item) => item !== value)
                )
              }}
              aria-label={`${label} 目标 ${LABEL_TEXT[value] || value}`}
            />
            {LABEL_TEXT[value] || value}
          </label>
        ))}
      </div>
      <Button
        size='sm'
        variant='outline'
        className='ml-auto'
        disabled={pending || !selected.length}
        onClick={() => onSave({ enabled, stream, fps, labels: selected })}
      >
        保存
      </Button>
    </div>
  )
}
