import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { LoadingButton } from '@/features/foundation/loading-button'
import { StatusBadge } from '@/features/foundation/status-badge'
import { Empty, Notices, QueryState, SelectField } from '@/features/foundation/ui'
import { ZonePanel } from './zones'
import { RulePanel } from './rules'

type Config = Schema<'DetectionConfig'>
type Runtime = Schema<'DetectionRuntime'>
type Channel = Schema<'Channel'>

const LABEL_TEXT: Record<string, string> = {
  person: '人', bicycle: '自行车', car: '轿车', motorcycle: '摩托车', bus: '巴士',
  truck: '卡车', cat: '猫', dog: '狗', bird: '鸟', horse: '马', sheep: '羊', cow: '牛', bear: '熊',
}

// The detector's vocabulary is broad; a row shows the common six and keeps the
// rest one click away instead of spraying the whole model across every row.
const COMMON_LABELS = ['person', 'car', 'bicycle', 'motorcycle', 'truck', 'bus']
const MORE_LABELS = ['cat', 'dog', 'bird', 'horse', 'sheep', 'cow', 'bear']

const ERROR_CODES: Record<string, string> = {
  detection_label_unsupported: '模型不支持所选目标类别',
  detection_publish_failed: '发布失败，已保留原配置',
}

const errorText = (error: unknown) => apiErrorText(error, ERROR_CODES)

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
          按通道启停智能检测并选择码流、帧率与目标类别。检测从媒体服务器内部取流，只做检测与抓拍，不接管录像。
        </p>
        <p className='mt-1 text-xs text-muted-foreground'>
          发布把当前设置写入检测服务并使其重载，失败时保留上一份配置。检测源指向通道当前取流的会话，<b>会话变化（重新取流、换源、服务重启）会由系统自动重新发布，无需手动操作</b>；只有你修改设置后需要手动发布，未发布前保持原样。
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
              <p>
                <StatusBadge
                  tone={runtime.data.runtime?.reachable ? 'success' : 'destructive'}
                >
                  {runtime.data.runtime?.reachable ? '运行中' : '不可达'}
                </StatusBadge>
              </p>
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
            {runtime.data.revision && (
              <p
                className={
                  runtime.data.revision.pending
                    ? 'mt-1 text-xs text-amber-600'
                    : 'mt-1 text-xs text-muted-foreground'
                }
              >
                {runtime.data.revision.pending
                  ? '有设置尚未发布'
                  : '与检测服务一致'}
              </p>
            )}
          </div>
        </div>
      )}
      {(!permitted.length && channels.data) ? (
        <Empty>没有可配置检测的通道，请联系管理员分配通道配置权限。</Empty>
      ) : (
        <>
          <div className='flex items-center gap-2'>
            <LoadingButton
              pending={pending}
              pendingText='发布中…'
              onClick={() =>
                void run(async () => {
                  await apiRequest('/api/v1/detection/publish', { method: 'POST' })
                }, '检测配置已发布')
              }
            >
              发布检测配置
            </LoadingButton>
            <span className='text-xs text-muted-foreground'>
              发布后检测服务会重载一次；失败时保留上一份配置。
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
  pending,
  onSave,
}: {
  channel: Channel
  config?: Config
  pending: boolean
  onSave: (next: { enabled: boolean; stream: string; fps: number; labels: string[] }) => void
}) {
  // The row must follow the stored configuration, not a snapshot taken at
  // mount: the page renders before the config query resolves, and React reuses
  // these component instances once it does, so state seeded from an initially
  // undefined config would silently show defaults for every channel.
  const [draft, setDraft] = useState<{ enabled: boolean; stream: string; fps: number; labels: string[] } | null>(null)
  const enabled = draft?.enabled ?? config?.enabled ?? false
  const stream = draft?.stream ?? config?.stream ?? 'sub'
  const fps = draft?.fps ?? config?.fps ?? 5
  const selected = draft?.labels ?? config?.labels ?? ['person']
  const setEnabled = (next: boolean) => setDraft({ enabled: next, stream, fps, labels: selected })
  const setStream = (next: string) => setDraft({ enabled, stream: next, fps, labels: selected })
  const setFps = (next: number) => setDraft({ enabled, stream, fps: next, labels: selected })
  const setSelected = (update: (current: string[]) => string[]) => setDraft({ enabled, stream, fps, labels: update(selected) })
  const label = `CH${String(channel.channel_no ?? 0).padStart(2, '0')} ${channel.channel_name}`
  const extra = selected.filter((value) => !COMMON_LABELS.includes(value))
  const summary =
    selected.length === 0
      ? '未选择'
      : selected.length <= 3
        ? selected.map((value) => LABEL_TEXT[value] || value).join('、')
        : `${selected.length} 类`

  function toggle(value: string, next: boolean) {
    setSelected((current) =>
      next ? [...new Set([...current, value])] : current.filter((item) => item !== value)
    )
  }

  return (
    <div className='space-y-2 rounded-xl border bg-card p-3 text-sm'>
      <div className='flex flex-wrap items-center gap-x-4 gap-y-2' data-testid='detection-row'>
        <span className='w-40 shrink-0 truncate font-medium'>{label}</span>
        <label className='flex items-center gap-2'>
          <Checkbox
            checked={enabled}
            onCheckedChange={(next) => setEnabled(next === true)}
            aria-label={`${label} 启用检测`}
          />
          启用检测
        </label>
        {/* Labels live in the row header via ariaLabel only: repeating the
            channel name on every control is what pushed these to two lines. */}
        <div className='flex items-center gap-2'>
          <span className='text-xs text-muted-foreground'>码流</span>
          <SelectField
            ariaLabel={`${label} 检测码流`}
            value={stream}
            onValueChange={(next) => setStream((next ?? 'sub') as 'main' | 'sub')}
            options={[
              { value: 'sub', label: '子码流' },
              { value: 'main', label: '主码流' },
            ]}
            className='w-24'
          />
        </div>
        <div className='flex items-center gap-2'>
          <span className='text-xs text-muted-foreground'>帧率</span>
          <SelectField
            ariaLabel={`${label} 检测帧率`}
            value={String(fps)}
            onValueChange={(next) => setFps(Number(next ?? 5))}
            options={[2, 5, 10, 15].map((value) => ({ value: String(value), label: `${value} fps` }))}
            className='w-20'
          />
        </div>
        <div className='flex items-center gap-2'>
          <span className='text-xs text-muted-foreground'>目标</span>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant='outline' size='sm' aria-label={`${label} 目标类别`}>
                {summary}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align='start' className='w-44'>
              <DropdownMenuLabel>常用目标</DropdownMenuLabel>
              {COMMON_LABELS.map((value) => (
                <DropdownMenuCheckboxItem
                  key={value}
                  checked={selected.includes(value)}
                  onSelect={(event) => event.preventDefault()}
                  onCheckedChange={(next) => toggle(value, next === true)}
                >
                  {LABEL_TEXT[value] || value}
                </DropdownMenuCheckboxItem>
              ))}
              <DropdownMenuLabel>其他</DropdownMenuLabel>
              {MORE_LABELS.map((value) => (
                <DropdownMenuCheckboxItem
                  key={value}
                  checked={selected.includes(value)}
                  onSelect={(event) => event.preventDefault()}
                  onCheckedChange={(next) => toggle(value, next === true)}
                >
                  {LABEL_TEXT[value] || value}
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
          {extra.length > 0 && (
            <span className='text-xs text-muted-foreground'>+{extra.length} 类</span>
          )}
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
        {draft && <StatusBadge tone='warning'>未保存</StatusBadge>}
      </div>
      <RuleRow label={label} channelID={channel.id ?? ''} enabled={enabled} />
    </div>
  )
}

/**
 * The per-channel zones and rules, behind a disclosure. They belong to the
 * channel's detection, but drawing zones is not part of setting up detection
 * for 32 channels, so the row stays one line until asked. The section is only
 * meaningful on an enabled channel: without detection there is no frame to
 * draw on and no events to match.
 */
function RuleRow({
  label,
  channelID,
  enabled,
}: {
  label: string
  channelID: string
  enabled: boolean
}) {
  const client = useQueryClient()
  const [open, setOpen] = useState(false)
  const refresh = () => void client.invalidateQueries()
  return (
    <div className='w-full basis-full'>
      <Button
        type='button'
        size='sm'
        variant='ghost'
        aria-expanded={open}
        aria-label={`${label} 区域与规则`}
        disabled={!channelID}
        onClick={() => setOpen(!open)}
      >
        {open ? '收起区域与规则' : '区域与规则'}
      </Button>
      {open && (
        <div className='mt-2 space-y-4 border-t pt-3'>
          {!enabled && (
            <p role='status' className='text-xs text-amber-600'>
              这个通道未启用检测：区域无法校准，规则也不会有事件可匹配。先启用并发布。
            </p>
          )}
          <section className='space-y-2'>
            <h4 className='text-sm font-medium'>区域</h4>
            <p className='text-xs text-muted-foreground'>
              区域只存储在 one-nvr 并交给检测服务，判定由检测服务完成；删除区域会一并删除引用它的规则。
            </p>
            <ZonePanel channelID={channelID} onChanged={refresh} />
          </section>
          <section className='space-y-2 border-t pt-3'>
            <h4 className='text-sm font-medium'>规则</h4>
            <RulePanel channelID={channelID} onChanged={refresh} />
          </section>
        </div>
      )}
    </div>
  )
}
