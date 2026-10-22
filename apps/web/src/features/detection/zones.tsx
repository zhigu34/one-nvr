import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { LoadingButton } from '@/features/foundation/loading-button'
import { SelectField } from '@/features/foundation/ui'
import { cn } from '@/lib/utils'
import { RulePanel } from './rules'

type Zone = Schema<'DetectionZone'>
type Point = Schema<'ZonePoint'>

// The detector renders every camera at a fixed size and stretches the source
// into it, so a drawing survives any resolution change; what it cannot survive
// is a change in the source's aspect ratio. The canvas is shown at the
// detector's own shape for the same reason: what the operator marks here is
// what the detector classifies.
export const DETECT_WIDTH = 640
export const DETECT_HEIGHT = 360

const CATEGORY_LABELS: Record<string, string> = {
  person: '人',
  car: '车',
  truck: '卡车',
  bus: '巴士',
  motorcycle: '摩托车',
  bicycle: '自行车',
  dog: '狗',
  cat: '猫',
}

/**
 * The expanded workbench: canvas left, zones and rules right, so drawing a
 * polygon and naming it never needs a page scroll between the two. On a
 * detection-enabled channel the frame loads itself once — an operator who
 * expands a row wants to draw, not to press "读取" first; the request fails
 * quietly when the config is not published yet (the server refuses with
 * frame_not_ready and the canvas explains what to do).
 */
export function ZoneWorkbench({
  channelID,
  detectionEnabled,
}: {
  channelID: string
  detectionEnabled: boolean
}) {
  const [nonce, setNonce] = useState(detectionEnabled ? 1 : 0)
  const [frameFailed, setFrameFailed] = useState(false)
  const zones = useAPI<Schema<'DetectionZonePage'>>(
    `/api/v1/detection/zones?channel_id=${channelID}`,
    !!channelID
  )
  const rules = useAPI<Schema<'DetectionRulePage'>>(
    `/api/v1/detection/rules?channel_id=${channelID}`,
    !!channelID
  )
  const [error, setError] = useState('')
  // The drawn vertices live here, above both the canvas and the form: the
  // canvas appends points, the form saves them, and the count on
  // "保存区域（N 点）" stays true to what is on the canvas.
  const [draft, setDraft] = useState<Point[]>([])
  const refresh = () => {
    void zones.refetch()
    void rules.refetch()
  }
  const items = zones.data?.items || []
  // The auto-load fires exactly once per expansion: the ref guards against
  // re-runs, and the nonce starts at 1 only on an enabled channel.
  const loaded = useRef(detectionEnabled)
  useEffect(() => {
    if (detectionEnabled && !loaded.current) {
      loaded.current = true
      setFrameFailed(false)
      setNonce(1)
    }
  }, [channelID, detectionEnabled])

  async function remove(zone: Zone) {
    setError('')
    try {
      await apiRequest(
        `/api/v1/detection/zones/${zone.id}?channel_id=${channelID}`,
        { method: 'DELETE' }
      )
      refresh()
    } catch (cause) {
      setError(apiErrorText(cause))
    }
  }

  return (
    <div className='grid grid-cols-12 gap-4 border-t border-border pt-3'>
      <div className='col-span-12 lg:col-span-7'>
        <div className='mb-2 flex flex-wrap items-center gap-2'>
          <Button
            size='sm'
            variant='outline'
            onClick={() => {
              setFrameFailed(false)
              setNonce((current) => current + 1)
            }}
          >
            {nonce ? '刷新检测画面' : '读取检测画面'}
          </Button>
          <span className='text-xs text-muted-foreground'>
            {DETECT_WIDTH}×{DETECT_HEIGHT} · 与检测器分类的图像一致
          </span>
        </div>
        <ZoneCanvas
          channelID={channelID}
          zones={items}
          nonce={nonce}
          frameFailed={frameFailed}
          onFrameFailed={setFrameFailed}
          detectionEnabled={detectionEnabled}
          draft={draft}
          onDraftChange={setDraft}
        />
      </div>
      <div className='col-span-12 flex flex-col gap-4 lg:col-span-5'>
        <section className='rounded-lg border border-border bg-card p-3'>
          <h4 className='text-sm font-medium'>区域</h4>
          <p className='mt-1 text-xs text-muted-foreground'>
            判定由检测服务完成；删除区域会一并删除引用它的规则。
          </p>
          <ZoneDraftForm
            channelID={channelID}
            draft={draft}
            onDraftChange={setDraft}
            onSaved={refresh}
          />
          {error && (
            <p role='alert' className='mt-2 text-sm text-destructive'>
              {error}
            </p>
          )}
          <div className='mt-3 space-y-1'>
            {zones.isPending ? null : !items.length ? (
              <p className='text-xs text-muted-foreground'>这个通道还没有区域。</p>
            ) : (
              items.map((zone) => (
                <div
                  key={zone.id}
                  data-testid='zone-row'
                  className='flex items-center gap-2 rounded-lg border p-2 text-sm'
                >
                  <span className='font-medium'>{zone.name}</span>
                  <span className='text-xs text-muted-foreground'>
                    {(zone.coordinates ?? []).length} 点
                    {zone.objects?.length
                      ? ` · ${zone.objects.map((value) => CATEGORY_LABELS[value] || value).join('/')}`
                      : ' · 全部类别'}
                    {zone.needs_calibration ? ' · 待校准' : ''}
                  </span>
                  <Button
                    size='sm'
                    variant='ghost'
                    className='ml-auto'
                    aria-label={`删除区域 ${zone.name}`}
                    onClick={() => void remove(zone)}
                  >
                    删除
                  </Button>
                </div>
              ))
            )}
          </div>
          {items.some((zone) => zone.needs_calibration) && (
            <p role='alert' className='mt-2 text-sm text-amber-600'>
              有区域是依据旧画面比例绘制的（琥珀色）：摄像头画面比例变化后，同一组归一化坐标指向的场景位置已经不同，请核对后重画。
            </p>
          )}
        </section>
        <section className='rounded-lg border border-border bg-card p-3'>
          <h4 className='text-sm font-medium'>规则</h4>
          <p className='mt-1 text-xs text-muted-foreground'>
            规则在事件入库时评估；命中会记进事件详情。
          </p>
          <div className='mt-2'>
            <RulePanel channelID={channelID} onChanged={refresh} />
          </div>
        </section>
      </div>
    </div>
  )
}

/**
 * AI-02: the zone canvas. The operator draws a polygon on a still of the frame
 * the detector analyzes; coordinates are sent normalized, and the server reads
 * the reference geometry from the channel's own source test. Judging whether a
 * target is inside the zone is the detector's job, never this component's.
 */
export function ZoneCanvas({
  channelID,
  zones,
  nonce,
  frameFailed,
  onFrameFailed,
  detectionEnabled = true,
  draft,
  onDraftChange,
}: {
  channelID: string
  zones: Zone[]
  nonce: number
  frameFailed: boolean
  onFrameFailed: (failed: boolean) => void
  detectionEnabled?: boolean
  draft: Point[]
  onDraftChange: (update: Point[] | ((current: Point[]) => Point[])) => void
}) {
  function toNormalized(event: ReactPointerEvent<HTMLDivElement>): Point {
    const rect = event.currentTarget.getBoundingClientRect()
    const x = Math.min(Math.max((event.clientX - rect.left) / rect.width, 0), 1)
    const y = Math.min(Math.max((event.clientY - rect.top) / rect.height, 0), 1)
    return { x: Number(x.toFixed(5)), y: Number(y.toFixed(5)) }
  }

  const polygon = draft.map((point) => `${point.x * 100},${point.y * 100}`).join(' ')
  // The muted, inert canvas: without detection there is nothing to draw
  // against, so the box collapses to a slim strip instead of a big black void.
  const inert = !detectionEnabled

  return (
    <div>
      <div
        data-testid='zone-canvas'
        className={cn(
          'relative w-full overflow-hidden rounded-lg border border-zinc-200 bg-black dark:border-zinc-800',
          !inert && 'cursor-crosshair',
          inert && 'pointer-events-none opacity-40'
        )}
        style={{
          aspectRatio: inert ? undefined : `${DETECT_WIDTH}/${DETECT_HEIGHT}`,
          maxHeight: inert ? undefined : '45vh',
          height: inert ? '3rem' : undefined,
        }}
        onPointerDown={(event) => {
          // The inert branch also refuses synthetic events: without detection
          // there is no frame to draw against, whatever dispatches the click.
          if (inert || event.button !== 0) return
          // The point is computed from the event before the state update: the
          // synthetic event is released once the handler returns, and a state
          // updater runs later.
          const point = toNormalized(event)
          onDraftChange((current) => [...current, point])
        }}
      >
        {!inert && nonce > 0 && !frameFailed ? (
          <img
            key={nonce}
            src={`/api/v1/detection/frame?channel_id=${channelID}&frame=${nonce}`}
            alt='检测画面'
            onError={() => onFrameFailed(true)}
            className='pointer-events-none absolute inset-0 h-full w-full object-fill'
          />
        ) : (
          <p className='pointer-events-none absolute inset-0 grid place-items-center px-6 text-center text-xs text-muted-foreground'>
            {inert
              ? ''
              : frameFailed || nonce === 0
                ? '检测画面暂时不可读：通道可能未发布到检测服务。点击上方「读取检测画面」重试；坐标按比例存储，与画面尺寸无关。'
                : ''}
          </p>
        )}
        {!inert && (
          <svg
            viewBox='0 0 100 100'
            preserveAspectRatio='none'
            className='absolute inset-0 h-full w-full'
          >
            {zones.map((zone) => (
              <polygon
                key={zone.id}
                data-testid='zone-shape'
                points={(zone.coordinates ?? [])
                  .map((point) => `${(point.x ?? 0) * 100},${(point.y ?? 0) * 100}`)
                  .join(' ')}
                className={
                  zone.needs_calibration
                    ? 'fill-amber-500/20 stroke-amber-400'
                    : 'fill-sky-500/20 stroke-sky-400'
                }
                strokeWidth={0.5}
              />
            ))}
            {draft.length > 0 && (
              <polygon points={polygon} className='fill-emerald-500/25 stroke-emerald-400' strokeWidth={0.5} />
            )}
            {draft.map((point, index) => (
              <circle key={index} cx={point.x * 100} cy={point.y * 100} r={0.9} className='fill-emerald-400' />
            ))}
          </svg>
        )}
      </div>
      {!inert && (
        <p className='mt-2 text-xs text-muted-foreground'>
          在画面上依次点击顶点，至少三点成面；服务端会拒绝自交与越界的多边形。
        </p>
      )}
    </div>
  )
}

/**
 * The zone form lives under the zones card on the right: name, category and
 * the save button. It keeps a draft of drawn points through the canvas, so
 * "保存区域（N 点）" always reflects what is on the canvas.
 */
function ZoneDraftForm({
  channelID,
  draft,
  onDraftChange,
  onSaved,
}: {
  channelID: string
  draft: Point[]
  onDraftChange: (update: Point[] | ((current: Point[]) => Point[])) => void
  onSaved: () => void
}) {
  const [name, setName] = useState('')
  const [objects, setObjects] = useState('')
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)

  async function save() {
    if (draft.length < 3) {
      setError('至少需要三个顶点：先在左侧画面上点击')
      return
    }
    if (!name.trim()) {
      setError('请填写区域名称')
      return
    }
    setPending(true)
    setError('')
    try {
      await apiRequest(
        '/api/v1/detection/zones',
        jsonRequest('POST', {
          channel_id: channelID,
          name: name.trim(),
          coordinates: draft,
          objects: objects ? [objects] : [],
        })
      )
      onDraftChange([])
      setName('')
      setObjects('')
      onSaved()
    } catch (cause) {
      setError(apiErrorText(cause))
    } finally {
      setPending(false)
    }
  }

  return (
    <div className='mt-2 space-y-2'>
      <div className='flex flex-wrap items-center gap-2'>
        <Input
          aria-label='区域名称'
          className='h-8 w-36 text-xs'
          placeholder='区域名称，如「门口」'
          value={name}
          maxLength={32}
          onChange={(event) => setName(event.target.value)}
        />
        <SelectField
          ariaLabel='区域关注类别'
          value={objects}
          onValueChange={(next) => setObjects(next ?? '')}
          options={[
            { value: '', label: '全部类别' },
            ...Object.entries(CATEGORY_LABELS).map(([value, label]) => ({ value, label })),
          ]}
          className='w-28'
        />
        <Button
          size='sm'
          variant='ghost'
          disabled={!draft.length}
          onClick={() => onDraftChange([])}
        >
          清除绘制点
        </Button>
      </div>
      <LoadingButton
        size='sm'
        pending={pending}
        disabled={draft.length < 3 || !name.trim()}
        pendingText='保存中…'
        onClick={() => void save()}
      >
        保存区域（{draft.length} 点）
      </LoadingButton>
      {error && (
        <p role='alert' className='text-sm text-destructive'>
          {error}
        </p>
      )}
    </div>
  )
}


