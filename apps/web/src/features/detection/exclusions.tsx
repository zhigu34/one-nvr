import { useState } from 'react'
import { Trash2 } from 'lucide-react'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { Notices } from '@/features/foundation/ui'
import { cn } from '@/lib/utils'
import { ZoneCanvas, type Point } from './zones'

type Exclusion = Schema<'DetectionExclusion'>

const ERROR_CODES: Record<string, string> = {
  exclusion_invalid: '排除规则不完整：至少画三个点，时段两端都要填',
  exclusion_version_conflict: '该规则已被他人修改，请刷新后重试',
}

/**
 * Time-windowed exclusions: "between these hours, this region's detections are
 * not reported".
 *
 * What this is for, concretely: at night an IR-lit parked scooter reads as a
 * person at 0.77 confidence, over and over, because the classifier has never seen
 * that shape under infrared. Raising the global threshold hides real people on
 * other cameras; a permanent mask hides them all day. A window plus a region
 * silences exactly the combination that misfires.
 *
 * What it is NOT: a way to stop detecting. The event is still recorded with its
 * reason, and the count beside each rule says how much it has suppressed — a rule
 * set too wide otherwise looks exactly like a quiet site.
 */
export function ExclusionEditor({
  channelID,
  detectionEnabled,
  initialDraft = [],
}: {
  channelID: string
  detectionEnabled: boolean
  /**
   * Seed points for the drawing. The canvas writes into this component's state,
   * so a test cannot draw; a prefilled draft is how the submit path is exercised
   * without a media pipeline.
   */
  initialDraft?: Point[]
}) {
  const rules = useAPI<{ items: Exclusion[] }>(
    `/api/v1/detection/exclusions?channel_id=${channelID}`,
    !!channelID
  )
  const [draft, setDraft] = useState<Point[]>(initialDraft)
  const [name, setName] = useState('')
  const [from, setFrom] = useState('22:00')
  const [to, setTo] = useState('07:00')
  const [categories, setCategories] = useState('person')
  const [reason, setReason] = useState('夜间误判')
  const [nonce, setNonce] = useState(detectionEnabled ? 1 : 0)
  const [frameFailed, setFrameFailed] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const items = rules.data?.items ?? []

  async function save() {
    if (pending) return
    setError('')
    setNotice('')
    if (draft.length < 3) {
      setError(ERROR_CODES.exclusion_invalid)
      return
    }
    setPending(true)
    try {
      await apiRequest(
        `/api/v1/detection/exclusions?channel_id=${channelID}`,
        jsonRequest('POST', {
          name: name.trim() || `排除 ${items.length + 1}`,
          coordinates: draft.map((point) => [point.x, point.y]),
          categories: categories
            .split(/[,，\s]+/)
            .map((value) => value.trim())
            .filter(Boolean),
          from_clock: from,
          to_clock: to,
          reason: reason.trim() || '时段内区域排除',
          enabled: true,
          version: 0,
        })
      )
      setDraft([])
      setName('')
      setNotice('排除规则已保存')
      await rules.refetch()
    } catch (cause) {
      setError(apiErrorText(cause, ERROR_CODES))
    } finally {
      setPending(false)
    }
  }

  async function remove(rule: Exclusion) {
    setPending(true)
    setError('')
    try {
      await apiRequest(
        `/api/v1/detection/exclusions/${rule.id}?channel_id=${channelID}`,
        jsonRequest('DELETE', undefined, rule.version)
      )
      setNotice('排除规则已删除；它已排除的事件保留原记录')
      await rules.refetch()
    } catch (cause) {
      setError(apiErrorText(cause, ERROR_CODES))
    } finally {
      setPending(false)
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
            在上图点出要静音的区域（至少三个点）
          </span>
        </div>
        <ZoneCanvas
          channelID={channelID}
          // No saved zones are drawn here: this canvas is about the region
          // being excluded, and overlaying the detection zones would make the
          // operator wonder which polygon they are editing.
          zones={[]}
          nonce={nonce}
          frameFailed={frameFailed}
          onFrameFailed={setFrameFailed}
          detectionEnabled={detectionEnabled}
          draft={draft}
          onDraftChange={setDraft}
        />
        <p className='mt-1 text-xs text-muted-foreground'>
          已点 {draft.length} 个点
          {draft.length > 0 && (
            <button
              type='button'
              className='ml-2 underline underline-offset-2 hover:text-foreground'
              onClick={() => setDraft([])}
            >
              清除
            </button>
          )}
        </p>
      </div>
      <div className='col-span-12 flex flex-col gap-3 lg:col-span-5'>
        <section className='rounded-lg border border-border bg-card p-3'>
          <h4 className='text-sm font-medium'>时段排除</h4>
          <p className='mt-1 text-xs text-muted-foreground'>
            只在这段时间内、这个区域里、这些类别不报告。事件仍会入库并标记原因，
            所以能看出规则是否设得太宽。
          </p>
          <div className='mt-3 grid gap-2'>
            <label className='grid gap-1 text-xs text-muted-foreground'>
              名称
              <Input
                aria-label='排除规则名称'
                value={name}
                maxLength={64}
                placeholder='如 夜间门前停车位'
                onChange={(event) => setName(event.target.value)}
              />
            </label>
            <div className='grid grid-cols-2 gap-2'>
              <label className='grid gap-1 text-xs text-muted-foreground'>
                从
                <input
                  aria-label='排除开始时间'
                  type='time'
                  value={from}
                  onChange={(event) => setFrom(event.target.value)}
                  className='h-9 rounded-md border border-input bg-transparent px-2 font-mono text-sm'
                />
              </label>
              <label className='grid gap-1 text-xs text-muted-foreground'>
                到
                <input
                  aria-label='排除结束时间'
                  type='time'
                  value={to}
                  onChange={(event) => setTo(event.target.value)}
                  className='h-9 rounded-md border border-input bg-transparent px-2 font-mono text-sm'
                />
              </label>
            </div>
            <p className='text-[11px] text-muted-foreground'>
              {from > to ? '跨午夜（例如 22:00 到次日 07:00）' : '同一天内'}
            </p>
            <label className='grid gap-1 text-xs text-muted-foreground'>
              类别（留空 = 全部）
              <Input
                aria-label='排除类别'
                value={categories}
                placeholder='person, car'
                onChange={(event) => setCategories(event.target.value)}
              />
            </label>
            <label className='grid gap-1 text-xs text-muted-foreground'>
              排除原因（显示在事件上）
              <Input
                aria-label='排除原因'
                value={reason}
                maxLength={64}
                onChange={(event) => setReason(event.target.value)}
              />
            </label>
            <Button
              size='sm'
              disabled={pending || draft.length < 3}
              onClick={() => void save()}
            >
              保存排除规则（{draft.length} 点）
            </Button>
          </div>
          <Notices error={error} notice={notice} />
        </section>

        <section className='rounded-lg border border-border bg-card p-3'>
          <h4 className='text-sm font-medium'>已有排除</h4>
          {!items.length ? (
            <p className='mt-1 text-xs text-muted-foreground'>
              这个通道还没有时段排除。
            </p>
          ) : (
            <ul className='mt-2 space-y-1.5'>
              {items.map((rule) => (
                <li
                  key={rule.id}
                  data-testid='exclusion-row'
                  className='flex flex-wrap items-center gap-2 rounded-md border px-2 py-1.5 text-sm'
                >
                  <span className='font-medium'>{rule.name}</span>
                  <span className='font-mono text-xs text-muted-foreground'>
                    {rule.from_clock || '全天'}–{rule.to_clock || '全天'}
                  </span>
                  <span className='text-xs text-muted-foreground'>
                    {rule.categories?.length
                      ? rule.categories.join('/')
                      : '全部类别'}
                  </span>
                  {/* The count is the whole reason a rule is trustworthy: a rule
                      that suppressed 2000 events is not doing what was intended
                      even though the page looks calm. */}
                  <span
                    data-testid='exclusion-suppressed'
                    title='最近 24 小时被这条规则排除的事件数'
                    className={cn(
                      'rounded px-1.5 py-0.5 font-mono text-[11px]',
                      (rule.suppressed_24h ?? 0) > 0
                        ? 'bg-amber-500/15 text-amber-600 dark:text-amber-400'
                        : 'bg-muted text-muted-foreground'
                    )}
                  >
                    24h 排除 {rule.suppressed_24h ?? 0}
                  </span>
                  <Button
                    size='sm'
                    variant='ghost'
                    className='ml-auto h-7 px-2 text-xs'
                    aria-label={`删除排除规则 ${rule.name}`}
                    disabled={pending}
                    onClick={() => void remove(rule)}
                  >
                    <Trash2 aria-hidden className='size-3.5' />
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
    </div>
  )
}
