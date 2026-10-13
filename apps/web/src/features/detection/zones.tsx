import { useState, type PointerEvent as ReactPointerEvent } from 'react'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { LoadingButton } from '@/features/foundation/loading-button'
import { QueryState, SelectField } from '@/features/foundation/ui'

type Zone = Schema<'DetectionZone'>
type Point = Schema<'ZonePoint'>

// The detector renders every camera at a fixed size and stretches the source
// into it, so a drawing survives any resolution change; what it cannot survive
// is a change in the source's aspect ratio. The canvas is shown at the
// detector's own shape for the same reason: what the operator marks here is
// what the detector classifies.
const DETECT_WIDTH = 640
const DETECT_HEIGHT = 360

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
 * AI-02: the zone canvas. The operator draws a polygon on a still of the frame
 * the detector analyzes; coordinates are sent normalized, and the server reads
 * the reference geometry from the channel's own source test. Judging whether a
 * target is inside the zone is the detector's job, never this component's.
 */
export function ZoneCanvas({
  channelID,
  zones,
  onSaved,
}: {
  channelID: string
  zones: Zone[]
  onSaved: () => void
}) {
  const [draft, setDraft] = useState<Point[]>([])
  const [name, setName] = useState('')
  const [objects, setObjects] = useState('')
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  // The still advances in the URL so "重新读取" re-fetches through the browser
  // cache, exactly like the event snapshot images.
  const [frameNonce, setFrameNonce] = useState(0)
  const [frameFailed, setFrameFailed] = useState(false)

  function toNormalized(event: ReactPointerEvent<HTMLDivElement>): Point {
    const rect = event.currentTarget.getBoundingClientRect()
    const x = Math.min(Math.max((event.clientX - rect.left) / rect.width, 0), 1)
    const y = Math.min(Math.max((event.clientY - rect.top) / rect.height, 0), 1)
    return { x: Number(x.toFixed(5)), y: Number(y.toFixed(5)) }
  }

  async function save() {
    if (draft.length < 3) {
      setError('至少需要三个顶点')
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
      setDraft([])
      setName('')
      setObjects('')
      onSaved()
    } catch (cause) {
      setError(apiErrorText(cause))
    } finally {
      setPending(false)
    }
  }

  const polygon = draft.map((point) => `${point.x * 100},${point.y * 100}`).join(' ')

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <Button
          size='sm'
          variant='outline'
          onClick={() => {
            setFrameFailed(false)
            setFrameNonce((current) => current + 1)
          }}
        >
          {frameNonce ? '重新读取画面' : '读取检测画面'}
        </Button>
        <span className='text-xs text-muted-foreground'>
          画面取自检测管道本身（{DETECT_WIDTH}×{DETECT_HEIGHT}，拉伸到检测尺寸），与检测器分类的图像一致。
        </span>
      </div>
      <div
        data-testid='zone-canvas'
        className='relative w-full cursor-crosshair overflow-hidden rounded-lg border bg-black'
        style={{ aspectRatio: `${DETECT_WIDTH}/${DETECT_HEIGHT}` }}
        onPointerDown={(event) => {
          if (event.button !== 0) return
          // The point is computed from the event before the state update: the
          // synthetic event is released once the handler returns, and a state
          // updater runs later.
          const point = toNormalized(event)
          setDraft((current) => [...current, point])
        }}
      >
        {frameNonce > 0 && !frameFailed ? (
          <img
            key={frameNonce}
            src={`/api/v1/detection/frame?channel_id=${channelID}&frame=${frameNonce}`}
            alt='检测画面'
            onError={() => setFrameFailed(true)}
            className='pointer-events-none absolute inset-0 h-full w-full object-fill'
          />
        ) : (
          <p className='pointer-events-none absolute inset-0 grid place-items-center px-6 text-center text-xs text-muted-foreground'>
            {frameFailed
              ? '检测画面暂时不可读：通道可能未启用检测，或尚未发布。仍可先绘制——坐标按比例存储，与画面尺寸无关。'
              : '还没有读取画面。可以先读取一张再绘制；坐标按比例存储，换成任何分辨率仍指向同一片区域。'}
          </p>
        )}
        {/* The overlay lives in the detector's coordinate space (0..100 on both
            axes) and is stretched with the frame, exactly as the detector
            stretches the source into its own frame. */}
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
      </div>
      <p className='text-xs text-muted-foreground'>
        在画面上依次点击顶点，至少三点成面；服务端会拒绝自交与越界的多边形。
      </p>
      <div className='flex flex-wrap items-center gap-2'>
        <Input
          aria-label='区域名称'
          className='w-44'
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
          className='w-32'
        />
        <LoadingButton
          size='sm'
          pending={pending}
          disabled={draft.length < 3}
          pendingText='保存中…'
          onClick={() => void save()}
        >
          保存区域（{draft.length} 点）
        </LoadingButton>
        <Button size='sm' variant='ghost' disabled={!draft.length} onClick={() => setDraft([])}>
          清除
        </Button>
      </div>
      {error && (
        <p role='alert' className='text-sm text-destructive'>
          {error}
        </p>
      )}
      {zones.some((zone) => zone.needs_calibration) && (
        <p role='alert' className='text-sm text-amber-600'>
          有区域是依据旧画面比例绘制的（琥珀色）：摄像头画面比例变化后，同一组归一化坐标指向的场景位置已经不同，请核对后重画。
        </p>
      )}
    </div>
  )
}

/**
 * The zone panel: the canvas plus the saved drawings, with deletion. A zone
 * bound to a rule takes the rule with it (the server cascades), which the
 * surrounding copy says plainly.
 */
export function ZonePanel({ channelID, onChanged }: { channelID: string; onChanged: () => void }) {
  const zones = useAPI<Schema<'DetectionZonePage'>>(
    `/api/v1/detection/zones?channel_id=${channelID}`,
    !!channelID
  )
  const [error, setError] = useState('')
  const items = zones.data?.items || []

  async function remove(zone: Zone) {
    setError('')
    try {
      await apiRequest(
        `/api/v1/detection/zones/${zone.id}?channel_id=${channelID}`,
        { method: 'DELETE' }
      )
      onChanged()
    } catch (cause) {
      setError(apiErrorText(cause))
    }
  }

  return (
    <div className='space-y-3'>
      <QueryState pending={zones.isPending} error={zones.error} retry={zones.refetch} />
      <ZoneCanvas channelID={channelID} zones={items} onSaved={onChanged} />
      {error && (
        <p role='alert' className='text-sm text-destructive'>
          {error}
        </p>
      )}
      {zones.data && (
        <div className='space-y-1'>
          {!items.length ? (
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
      )}
    </div>
  )
}
