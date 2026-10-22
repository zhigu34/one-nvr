import { useMemo, useState } from 'react'
import { Check } from 'lucide-react'
import { ApiError, apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useAction } from '@/features/foundation/hooks'
import { SelectField } from '@/features/foundation/ui'
import { cn } from '@/lib/utils'
import {
  COMMON_LABELS,
  LABEL_TEXT,
  MORE_LABELS,
  emptyDetectionDraft,
  type DetectionDraft,
  type Sensitivity,
} from './draft'

type Config = Schema<'DetectionConfig'>

const FPS_OPTIONS = [2, 5, 10, 15].map((value) => ({
  value: String(value),
  label: `${value} fps`,
}))

const STREAM_OPTIONS = [
  { value: 'sub', label: '子码流' },
  { value: 'main', label: '主码流' },
]

const SENSITIVITY_OPTIONS = [
  { value: 'sensitive', label: '灵敏' },
  { value: 'balanced', label: '标准' },
  { value: 'strict', label: '严格' },
]

/**
 * The page's single configuration surface for detection.
 *
 * Every detection setting is made here and applied to whatever is selected: the
 * switch, the stream, the frame rate, the sensitivity, the target labels. The
 * table below only reports what each channel ended up with.
 *
 * Why one bar rather than controls per row: sixteen rows each carrying five
 * selects is sixteen places to change one setting, and five of them are almost
 * always identical across the site — an operator sets "sub stream, 5 fps,
 * standard, person" once and expects every camera to follow.
 *
 * The draft records only what was CHANGED. Applying writes the changed fields and
 * leaves the rest at each channel's stored value, so a batch never silently
 * resets a per-camera difference the operator did not touch. Selecting exactly
 * one channel echoes its stored settings, so editing one channel is the same
 * gesture as editing sixteen.
 */
export function DetectionConfigBar({
  chosen,
  chosenCount,
  total,
  onSelectAll,
  onClear,
  configOf,
}: {
  chosen: Schema<'Channel'>[]
  chosenCount: number
  total: number
  onSelectAll: () => void
  onClear: () => void
  configOf: Map<string, Config>
}) {
  const action = useAction()
  const [outcome, setOutcome] = useState('')
  // Only the fields the operator touched. `null` means "leave every channel's own
  // value alone", which is what makes a batch safe.
  const [draft, setDraft] = useState<DetectionDraft>(() => emptyDetectionDraft())

  const solo = chosenCount === 1 ? chosen[0] : undefined
  const soloConfig = solo ? configOf.get(solo.id ?? '') : undefined
  // The echo is an initial value, not an override — the same rule the recording
  // plan bar follows. Treating it as authoritative made the controls look dead:
  // a click changed local state that the next render replaced.
  const echoed = useMemo(() => {
    if (!solo) return null
    return {
      enabled: soloConfig?.enabled ?? false,
      stream: soloConfig?.stream ?? 'sub',
      fps: soloConfig?.fps ?? 5,
      sensitivity: (soloConfig?.sensitivity ?? 'balanced') as Sensitivity,
      labels: soloConfig?.labels ?? ['person'],
    }
  }, [solo, soloConfig])

  const effective = {
    enabled: draft.enabled ?? echoed?.enabled ?? false,
    stream: draft.stream ?? echoed?.stream ?? 'sub',
    fps: draft.fps ?? echoed?.fps ?? 5,
    sensitivity: draft.sensitivity ?? echoed?.sensitivity ?? 'balanced',
    labels: draft.labels ?? echoed?.labels ?? ['person'],
  }
  const hasDraft =
    draft.enabled !== null ||
    draft.stream !== null ||
    draft.fps !== null ||
    draft.sensitivity !== null ||
    draft.labels !== null
  const canApply = chosenCount > 0 && hasDraft

  function patch(next: Partial<DetectionDraft>) {
    setOutcome('')
    setDraft((current) => ({ ...current, ...next }))
  }

  function toggleLabel(value: string, on: boolean) {
    patch({
      labels: on
        ? [...new Set([...effective.labels, value])]
        : effective.labels.filter((item) => item !== value),
    })
  }

  /**
   * Apply the bar to every selected channel.
   *
   * One PUT per channel, sequential: every batch on this product writes in order
   * because parallel writes race each other's version checks, and each write has
   * its own audit row.
   *
   * Each channel keeps its own values for every field the operator did not touch.
   * Sending the whole bar to every channel would reset a camera whose stream or
   * label set was deliberately different — the bar is a change, not a template.
   */
  async function apply() {
    if (!canApply) return
    setOutcome('')
    const ok: string[] = []
    const failed: string[] = []
    await action.run(async () => {
      for (const channel of chosen) {
        const label = `CH${String(channel.channel_no).padStart(2, '0')}`
        const stored = configOf.get(channel.id ?? '')
        const body = {
          enabled: draft.enabled ?? stored?.enabled ?? false,
          stream: draft.stream ?? stored?.stream ?? 'sub',
          fps: draft.fps ?? stored?.fps ?? 5,
          sensitivity: draft.sensitivity ?? stored?.sensitivity ?? 'balanced',
          labels: draft.labels ?? stored?.labels ?? ['person'],
          version: stored?.version ?? 0,
        }
        // A channel without a stored config has version 0 and is created here.
        // Detection on a channel with no camera is refused by the server; report
        // it rather than letting it abort the batch.
        if (!body.labels.length) {
          failed.push(`${label}（未选择目标）`)
          continue
        }
        try {
          await apiRequest(
            `/api/v1/detection/configs/${channel.id}`,
            jsonRequest('PUT', body)
          )
          ok.push(label)
        } catch (cause) {
          const reason = cause instanceof ApiError ? cause.message : '失败'
          failed.push(`${label}（${reason}）`)
        }
      }
      setOutcome(
        failed.length
          ? `已应用 ${ok.length} 路；未应用：${failed.join('、')}`
          : `已应用 ${ok.length} 路检测配置`
      )
    }, '')
    if (action.error) return
    if (ok.length) setDraft(emptyDetectionDraft())
  }

  return (
    <div className='space-y-3 rounded-lg border border-border bg-muted/30 p-4'>
      {/* Selection line: what the bar will act on, and the two ways to change
          that set. Kept apart from the settings row so "how many" never sits
          between two controls that look like they might depend on it. */}
      <div className='flex flex-wrap items-center gap-2'>
        <span className='text-sm font-medium tabular-nums'>
          已选 {chosenCount} / {total} 路
        </span>
        <Button
          type='button'
          size='sm'
          variant='ghost'
          onClick={onSelectAll}
          disabled={total === 0}
        >
          全选
        </Button>
        <Button
          type='button'
          size='sm'
          variant='ghost'
          onClick={onClear}
          disabled={chosenCount === 0}
        >
          清空
        </Button>
      </div>

      {/* The settings, on one baseline. Every control carries the same height
          (h-9): a switch, three selects and a dropdown trigger are four
          different widgets, and any difference in their height reads as a
          difference in their importance. The height is set per control rather
          than with a descendant selector — the dropdown's menu also lives in
          this subtree, and a blanket rule would stretch every menu item. */}
      <div className='flex flex-wrap items-center gap-x-3 gap-y-2'>
        <label className='inline-flex h-9 items-center gap-2 pr-1 text-sm'>
          <Switch
            checked={effective.enabled}
            aria-label='批量启用检测'
            onCheckedChange={(next) => patch({ enabled: next })}
          />
          启用检测
        </label>

        <div className='inline-flex items-center gap-1.5'>
          <span className='text-xs text-muted-foreground'>码流</span>
          <SelectField
            ariaLabel='批量检测码流'
            value={effective.stream}
            onValueChange={(next) => patch({ stream: next ?? 'sub' })}
            options={STREAM_OPTIONS}
            className='w-24'
            triggerClassName='h-9'
          />
        </div>
        <div className='inline-flex items-center gap-1.5'>
          <span className='text-xs text-muted-foreground'>帧率</span>
          <SelectField
            ariaLabel='批量检测帧率'
            value={String(effective.fps)}
            onValueChange={(next) => patch({ fps: Number(next ?? 5) })}
            options={FPS_OPTIONS}
            className='w-20'
            triggerClassName='h-9'
          />
        </div>
        <div className='inline-flex items-center gap-1.5'>
          <span className='text-xs text-muted-foreground'>灵敏度</span>
          <SelectField
            ariaLabel='批量检测灵敏度'
            value={effective.sensitivity}
            onValueChange={(next) =>
              patch({ sensitivity: (next ?? 'balanced') as Sensitivity })
            }
            options={SENSITIVITY_OPTIONS}
            className='w-24'
            triggerClassName='h-9'
          />
        </div>
        <div className='inline-flex items-center gap-1.5'>
          <span className='text-xs text-muted-foreground'>目标</span>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant='outline' size='sm' className='h-9' aria-label='批量目标类别'>
                {effective.labels.length === 0
                  ? '未选择'
                  : effective.labels.length <= 3
                    ? effective.labels
                        .map((value) => LABEL_TEXT[value] || value)
                        .join('、')
                    : `${effective.labels.length} 类`}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align='start' className='w-44'>
              <DropdownMenuLabel>常用目标</DropdownMenuLabel>
              {COMMON_LABELS.map((value) => (
                <DropdownMenuCheckboxItem
                  key={value}
                  checked={effective.labels.includes(value)}
                  onSelect={(event) => event.preventDefault()}
                  onCheckedChange={(next) => toggleLabel(value, next === true)}
                >
                  {LABEL_TEXT[value] || value}
                </DropdownMenuCheckboxItem>
              ))}
              <DropdownMenuLabel>其他</DropdownMenuLabel>
              {MORE_LABELS.map((value) => (
                <DropdownMenuCheckboxItem
                  key={value}
                  checked={effective.labels.includes(value)}
                  onSelect={(event) => event.preventDefault()}
                  onCheckedChange={(next) => toggleLabel(value, next === true)}
                >
                  {LABEL_TEXT[value] || value}
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>

        <span className='mx-1 hidden h-5 w-px bg-border sm:block' />

        {/* Disabled with a reason attached rather than explained by a sentence
            below: the operator's cursor is already here when they wonder why
            the button is grey, so the answer belongs on the button. */}
        <Button
          type='button'
          size='sm'
          className={cn(
            'h-9 transition-opacity',
            canApply
              ? 'bg-emerald-500 font-medium text-zinc-950 hover:bg-emerald-400'
              : 'opacity-40'
          )}
          disabled={!canApply || action.pending}
          title={
            canApply
              ? undefined
              : chosenCount === 0
                ? '先在下方勾选要修改的通道'
                : '先修改上面任意一项设置'
          }
          onClick={() => void apply()}
        >
          <Check aria-hidden className='size-3.5' />
          应用到所选通道{chosenCount > 0 ? ` (${chosenCount})` : ''}
        </Button>
      </div>

      <p className='border-t border-border/60 pt-2.5 text-xs leading-relaxed text-muted-foreground/80'>
        只写入改动过的项，其余保持各通道原值；选一路时回显它当前设置。
        保存后仍需点「发布检测配置」才会生效。
      </p>
      {outcome && (
        <p role='status' className='text-xs text-muted-foreground'>
          {outcome}
        </p>
      )}
      {action.error && (
        <p role='alert' className='text-xs text-destructive'>
          {action.error}
        </p>
      )}
    </div>
  )
}
