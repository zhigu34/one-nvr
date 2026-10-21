import { useState } from 'react'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { configuredChannels } from '@/features/foundation/channels'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { LoadingButton } from '@/features/foundation/loading-button'
import { StatusBadge, type StatusTone } from '@/features/foundation/status-badge'
import { CheckboxField, Empty, Field, QueryState, SelectField } from '@/features/foundation/ui'

type Rule = Schema<'DetectionRule'>
type Zone = Schema<'DetectionZone'>
type Channel = Schema<'Channel'>

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

const KIND_LABELS: Record<string, string> = {
  appear: '目标出现',
  enter: '区域进入',
  dwell: '区域停留',
}

// The detector keeps one dwell threshold per zone and applies it to both
// "present" and "entered", so these two refusals describe a real detector
// constraint rather than a form rule.
const ERROR_CODES: Record<string, string> = {
  rule_dwell_conflict: '同一区域只能有一个停留阈值，请改用已有秒数或换一个区域',
  rule_enter_on_dwell_zone:
    '该区域已有停留规则：检测器会把「进入」也延后到停留时长之后。请为该区域改用停留规则，或另建区域做进入判定',
}

const SEVERITY_LABELS: Record<string, string> = {
  low: '低',
  medium: '中',
  high: '高',
}

const SEVERITY_TONES: Record<string, StatusTone> = {
  low: 'neutral',
  medium: 'info',
  high: 'destructive',
}

/**
 * AI-03/AI-04: the rule editor. A rule is a business question ("tell me about
 * people entering the gate at night"), not a detection parameter — category,
 * score floor, arming window and severity are all evaluated here, while the
 * zone semantics (inertia, loitering) stay with the detector. A rule that could
 * never fire is refused by the server with a reason instead of being stored.
 */
export function RulePanel({ channelID, onChanged }: { channelID: string; onChanged: () => void }) {
  const rules = useAPI<Schema<'DetectionRulePage'>>(
    `/api/v1/detection/rules?channel_id=${channelID}`,
    !!channelID
  )
  const zones = useAPI<Schema<'DetectionZonePage'>>(
    `/api/v1/detection/zones?channel_id=${channelID}`,
    !!channelID
  )
  const channels = useAPI<PageData<Channel>>('/api/v1/channels?limit=100')
  const permitted = configuredChannels(channels.data?.items || []).some(
    (channel) => channel.id === channelID && channel.permissions.includes('configure')
  )
  // Enabled destinations only: the server skips a disabled channel at raise
  // time, so offering it here would let an operator bind an alert path that
  // silently tells no one.
  const notificationChannels = useAPI<{ items: Schema<'NotificationChannel'>[] }>(
    '/api/v1/notification-channels',
    !!channelID && permitted
  )
  const channelsForBinding = (notificationChannels.data?.items || []).filter(
    (channel) => channel.enabled !== false
  )
  const [editing, setEditing] = useState<Rule | null>(null)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  // Which notification channels the rule being edited tells. Held beside the
  // draft because the binding is a separate resource: the rule says what to
  // watch, the binding says who hears about it.
  const [binding, setBinding] = useState<string[]>([])
  const items = rules.data?.items || []
  const zoneItems = zones.data?.items || []

  function startNew() {
    setBinding([])
    setEditing({
      channel_id: channelID,
      name: '',
      kind: 'appear',
      categories: ['person'],
      min_score: 0,
      arm_from: '',
      arm_to: '',
      dwell_seconds: null,
      severity: 'medium',
      cooldown_seconds: 60,
      enabled: true,
    })
  }

  async function save(rule: Rule) {
    setPending(true)
    setError('')
    try {
      const saved = await apiRequest<Rule>('/api/v1/detection/rules', jsonRequest('POST', rule))
      // The binding is written second because it needs the rule's id. A failure
      // here keeps the editor open rather than pretending the whole save
      // succeeded: the rule itself is already stored and re-saving updates it.
      await apiRequest(
        `/api/v1/detection/rules/${saved?.id ?? rule.id}/channels`,
        jsonRequest('PUT', { channel_ids: binding })
      )
      setEditing(null)
      onChanged()
    } catch (cause) {
      setError(apiErrorText(cause, ERROR_CODES))
    } finally {
      setPending(false)
    }
  }

  // Opening a rule loads which channels it tells, so the editor shows the
  // stored binding rather than an empty selection that would erase it on save.
  function openRule(rule: Rule) {
    setEditing(rule)
    setBinding([])
    if (!rule.id) return
    void apiRequest<{ channel_ids?: string[] }>(`/api/v1/detection/rules/${rule.id}/channels`)
      .then((result) => setBinding(result?.channel_ids ?? []))
      .catch(() => setBinding([]))
  }

  async function remove(rule: Rule) {
    setError('')
    try {
      await apiRequest(`/api/v1/detection/rules/${rule.id}?channel_id=${channelID}`, {
        method: 'DELETE',
      })
      onChanged()
    } catch (cause) {
      setError(apiErrorText(cause, ERROR_CODES))
    }
  }

  return (
    <div className='space-y-3'>
      <QueryState
        pending={rules.isPending || zones.isPending}
        error={rules.error || zones.error}
        retry={rules.refetch}
      />
      <div className='flex items-center gap-2'>
        <Button size='sm' variant='outline' disabled={!permitted} onClick={startNew}>
          新建规则
        </Button>
        <span className='text-xs text-muted-foreground'>
          规则在事件入库时评估；命中会记进事件详情，冷却只限制提醒频率，不丢事件。
        </span>
      </div>
      {error && (
        <p role='alert' className='text-sm text-destructive'>
          {error}
        </p>
      )}
      {rules.data && !items.length && !editing && (
        <Empty>这个通道还没有规则。点「新建规则」开始配置；规则决定哪些检测会被记录并提醒。</Empty>
      )}
      <div className='space-y-1'>
        {items.map((rule) => (
          <div
            key={rule.id}
            data-testid='rule-row'
            className='flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border p-2 text-sm'
          >
            <span className='font-medium'>{rule.name}</span>
            <span className='text-xs text-muted-foreground'>{KIND_LABELS[rule.kind ?? ''] || rule.kind}</span>
            <StatusBadge tone={SEVERITY_TONES[rule.severity ?? 'medium'] || 'neutral'}>
              {SEVERITY_LABELS[rule.severity ?? 'medium'] || rule.severity}
            </StatusBadge>
            <span className='text-xs text-muted-foreground'>
              {(rule.categories || []).map((value) => CATEGORY_LABELS[value] || value).join('、')}
              {rule.zone_id
                ? ` · ${zoneItems.find((zone) => zone.id === rule.zone_id)?.name || '区域已删除'}`
                : ''}
              {rule.dwell_seconds ? ` · 停留 ${rule.dwell_seconds} 秒` : ''}
              {rule.arm_from ? ` · ${rule.arm_from}–${rule.arm_to}` : ' · 全天布防'}
              {rule.min_score ? ` · 置信 ≥ ${Math.round(rule.min_score * 100)}%` : ''}
              {rule.cooldown_seconds ? ` · 冷却 ${rule.cooldown_seconds} 秒` : ''}
              {rule.enabled ? '' : ' · 已停用'}
            </span>
            <div className='ml-auto flex gap-1'>
              <Button
                size='sm'
                variant='ghost'
                aria-label={`编辑规则 ${rule.name}`}
                onClick={() => openRule(rule)}
              >
                编辑
              </Button>
              <Button
                size='sm'
                variant='ghost'
                aria-label={`删除规则 ${rule.name}`}
                onClick={() => void remove(rule)}
              >
                删除
              </Button>
            </div>
          </div>
        ))}
      </div>
      {editing && (
        <div className='space-y-2'>
          <p className='text-sm font-medium'>
            {editing.id ? '编辑规则' : '新建规则'}
          </p>
          <RuleEditor
          rule={editing}
          zones={zoneItems}
          allRules={items}
          channels={channels.data?.items ? channelsForBinding : []}
          binding={binding}
          pending={pending}
          onCancel={() => setEditing(null)}
          onSave={save}
          onBinding={setBinding}
          />
        </div>
      )}
    </div>
  )
}

function RuleEditor({
  rule,
  zones,
  allRules,
  channels,
  binding,
  pending,
  onCancel,
  onSave,
  onBinding,
}: {
  rule: Rule
  zones: Zone[]
  allRules: Rule[]
  channels: Schema<'NotificationChannel'>[]
  binding: string[]
  pending: boolean
  onCancel: () => void
  onSave: (rule: Rule) => void
  onBinding: (channelIDs: string[]) => void
}) {
  const [draft, setDraft] = useState<Rule>(rule)
  const update = (patch: Partial<Rule>) => setDraft((current) => ({ ...current, ...patch }))
  // A rule's shape decides which fields mean anything: an appear rule must not
  // name a zone, enter and dwell must, and dwell needs its threshold. The form
  // shows only what applies so a rule that cannot fire is hard to write.
  const needsZone = draft.kind === 'enter' || draft.kind === 'dwell'
  const zoneID = draft.zone_id ?? ''
  // The detector keeps one dwell threshold per zone and applies it to both
  // "present" and "entered": flagging the conflict here saves a round trip and
  // explains the constraint where it is being violated.
  const dwellOnZone = allRules.find(
    (other) => other.id !== draft.id && other.zone_id === zoneID && other.kind === 'dwell'
  )
  const enterOnZone = allRules.find(
    (other) => other.id !== draft.id && other.zone_id === zoneID && other.kind === 'enter'
  )
  const blocked =
    !!zoneID &&
    ((draft.kind === 'enter' && !!dwellOnZone) ||
      (draft.kind === 'dwell' && (!!enterOnZone || !!dwellOnZone)))
  const blockedText =
    draft.kind === 'dwell' && dwellOnZone
      ? `该区域已有停留规则「${dwellOnZone.name}」（${dwellOnZone.dwell_seconds} 秒）：同一区域只能有一个停留阈值`
      : `该区域已有${draft.kind === 'dwell' ? '进入' : '停留'}规则「${(draft.kind === 'dwell' ? enterOnZone : dwellOnZone)?.name}」：检测器对同一区域只保留一个进入判定，进入会被停留阈值延后`

  return (
    <div data-testid='rule-editor' className='space-y-3 rounded-xl border bg-card p-3'>
      <div className='grid gap-3 sm:grid-cols-2'>
        <Field
          label='规则名称'
          name='rule-name'
          value={draft.name ?? ''}
          maxLength={64}
          placeholder='如「非工作时间有人」'
          onChange={(event) => update({ name: event.target.value })}
        />
        <SelectField
          label='规则类型'
          ariaLabel='规则类型'
          value={draft.kind ?? 'appear'}
          onValueChange={(next) =>
            update({
              kind: (next || 'appear') as Rule['kind'],
              // Clearing the zone on switch keeps the payload valid: an appear
              // rule carrying a zone is refused by the server.
              zone_id: next === 'appear' ? null : (draft.zone_id ?? null),
              dwell_seconds: next === 'dwell' ? (draft.dwell_seconds ?? 5) : null,
            })
          }
          options={Object.entries(KIND_LABELS).map(([value, label]) => ({ value, label }))}
        />
        <fieldset className='grid gap-2 sm:col-span-2'>
          <legend className='text-sm text-muted-foreground'>目标类别</legend>
          <div className='flex flex-wrap gap-x-4 gap-y-2'>
            {Object.entries(CATEGORY_LABELS).map(([value, label]) => (
              <CheckboxField
                key={value}
                label={label}
                checked={(draft.categories ?? []).includes(value)}
                onCheckedChange={(checked) => {
                  const current = draft.categories ?? []
                  const next = checked
                    ? [...current, value]
                    : current.filter((entry) => entry !== value)
                  // An empty selection would be refused by the server, and a
                  // rule that matches nothing is not what unticking the last box
                  // means. Keep the last one so the form stays valid.
                  update({ categories: next.length ? next : current })
                }}
              />
            ))}
          </div>
          <p className='text-xs text-muted-foreground'>
            可多选；想覆盖全部类别时逐个勾选。
          </p>
        </fieldset>
        <SelectField
          label='严重级别'
          ariaLabel='严重级别'
          value={draft.severity ?? 'medium'}
          onValueChange={(next) => update({ severity: (next || 'medium') as Rule['severity'] })}
          options={Object.entries(SEVERITY_LABELS).map(([value, label]) => ({ value, label }))}
        />
        {needsZone && (
          <SelectField
            label='区域'
            ariaLabel='区域'
            value={zoneID}
            onValueChange={(next) => update({ zone_id: next || null })}
            options={[
              { value: '', label: zones.length ? '选择区域' : '尚无区域，请先绘制' },
              ...zones.map((zone) => ({ value: zone.id ?? '', label: zone.name ?? '' })),
            ]}
          />
        )}
        {draft.kind === 'dwell' && (
          <Field
            label='停留多久算停留（秒）'
            name='rule-dwell'
            type='number'
            min={1}
            max={86400}
            value={draft.dwell_seconds ?? 5}
            onChange={(event) => update({ dwell_seconds: Number(event.target.value) })}
          />
        )}
        <Field
          label='置信度下限（%）'
          name='rule-min-score'
          type='number'
          min={0}
          max={100}
          placeholder='0 表示不限制'
          value={Math.round((draft.min_score ?? 0) * 100)}
          onChange={(event) => update({ min_score: Number(event.target.value) / 100 })}
        />
        <Field
          label='提醒冷却（秒）'
          name='rule-cooldown'
          type='number'
          min={0}
          max={86400}
          value={draft.cooldown_seconds ?? 60}
          onChange={(event) => update({ cooldown_seconds: Number(event.target.value) })}
        />
        <Field
          label='布防开始（HH:MM）'
          name='rule-arm-from'
          placeholder='留空 = 全天'
          value={draft.arm_from ?? ''}
          maxLength={5}
          onChange={(event) => update({ arm_from: event.target.value })}
        />
        <Field
          label='布防结束（HH:MM）'
          name='rule-arm-to'
          placeholder='早于开始 = 跨午夜'
          value={draft.arm_to ?? ''}
          maxLength={5}
          onChange={(event) => update({ arm_to: event.target.value })}
        />
      </div>
      {blocked && (
        <p role='alert' className='text-sm text-amber-600'>
          {blockedText}
        </p>
      )}
      <fieldset className='grid gap-2'>
        <legend className='text-sm text-muted-foreground'>通知渠道</legend>
        {channels.length === 0 ? (
          <p className='text-xs text-muted-foreground'>
            尚未配置通知渠道。到「系统设置 → 通知渠道」添加后，这里即可选择；不选时规则只记录命中，不对外提醒。
          </p>
        ) : (
          <div className='flex flex-wrap gap-x-4 gap-y-2'>
            {channels.map((channel) => (
              <CheckboxField
                key={channel.id}
                label={channel.name ?? ''}
                checked={binding.includes(channel.id ?? '')}
                onCheckedChange={(checked) =>
                  onBinding(
                    checked
                      ? [...binding, channel.id ?? '']
                      : binding.filter((entry) => entry !== channel.id)
                  )
                }
              />
            ))}
          </div>
        )}
        <p className='text-xs text-muted-foreground'>
          不勾选任何渠道时，规则命中仍会被记录，但不会外发通知；站内提醒始终记录，不受冷却限制。
        </p>
      </fieldset>
      <div className='flex flex-wrap items-center gap-3'>
        <CheckboxField
          label='启用'
          checked={draft.enabled ?? true}
          onCheckedChange={(next) => update({ enabled: next })}
        />
        <span className='text-xs text-muted-foreground'>
          布防时段按站点时区；结束早于开始表示跨午夜（如 22:00–06:00）。
        </span>
        <div className='ml-auto flex gap-2'>
          <Button size='sm' variant='ghost' onClick={onCancel}>
            取消
          </Button>
          <LoadingButton
            size='sm'
            pending={pending}
            disabled={!draft.name?.trim() || (needsZone && !draft.zone_id) || blocked}
            pendingText='保存中…'
            onClick={() => onSave(draft)}
          >
            保存规则
          </LoadingButton>
        </div>
      </div>
    </div>
  )
}
