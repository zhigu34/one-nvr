import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, ApiError, jsonRequest } from '@/lib/api-client'
import type { PageData, Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Eye, EyeOff, Plus } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Input } from '@/components/ui/input'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Checkbox } from '@/components/ui/checkbox'
import { useAPI } from '@/features/foundation/hooks'
import { apiErrorText } from '@/features/foundation/api-errors'
import { Empty, Notices, QueryState, SelectField } from '@/features/foundation/ui'
import { RunStatus } from './archive-run-status'
import { MODE_LABEL, coverage, type ChannelStatus, type TargetStatus } from './archive-status'

type Target = Schema<'ArchiveTarget'>
type Capabilities = Schema<'ArchiveTargetCapabilities'>
type Policy = Schema<'ArchivePolicy'>

const ERROR_CODES: Record<string, string> = {
  archive_target_referenced: '该目标已有归档副本，只能停用不能删除',
  // The three probe verdicts are NOT mapped here on purpose: the server words
  // them with the stage and evidence it observed, and re-wording them in the
  // browser would throw that away. A local entry for the same code silently wins
  // over the server's message (see apiErrorText), which is how "连接失败" used to
  // hide whether the address, the credentials, or the root path was wrong.
}

const errorText = (error: unknown) => apiErrorText(error, ERROR_CODES)

/**
 * bundledConsoleUrl builds the link into the bundled OpenList console.
 *
 * The host comes from the browser, not the server: one-nvr knows only the port
 * it published, while the address the operator reached it on (a NAS name, an IP,
 * a tunnel) exists only in the address bar. Reusing the page's own hostname is
 * therefore the only answer that is right in every deployment.
 *
 * The console speaks plain HTTP on its own port, so the URL keeps the page's
 * scheme out of it — an https page cannot embed or open http content reliably,
 * but this is a navigation target, not an embed, and the operator's own
 * deployment decides whether TLS fronts it.
 */
function bundledConsoleUrl(hostname: string, port: number | undefined): string {
  if (!port) return ''
  return `http://${hostname}:${port}/`
}

const EMPTY_FORM = { name: '', base_url: '', remote_root: '', username: '', password: '', cloud_retention_days: 365 }

/**
 * ARC-01: the WebDAV archive target list. Admin only — this is site
 * infrastructure, and the upload grant is a separate per-channel decision.
 */
export function ArchiveTargets() {
  const admin = useAuthStore((state) => state.user?.role === 'admin')
  const client = useQueryClient()
  const targets = useAPI<PageData<Target>>('/api/v1/archive-targets', admin)
  const channels = useAPI<PageData<Schema<'Channel'>>>('/api/v1/channels?limit=100', admin)
  const policies = useAPI<{ items: Policy[] }>('/api/v1/archive-policies', admin)
  const targetStatus = useAPI<{ items: TargetStatus[] }>('/api/v1/archive-targets/status', admin, 15_000)
  const channelStatus = useAPI<{ items: ChannelStatus[] }>('/api/v1/archive-channels', admin, 30_000)
  // The bundled console's state and published port. Its own endpoint rather than
  // the component list: the list has no port, and the port is the whole point of
  // the link below.
  const bundledQuery = useAPI<Schema<'BundledOpenList'>>('/api/v1/operations/bundled-openlist', admin, 30_000)
  const bundled = bundledQuery.data
  const site = useAPI<Schema<'Site'>>('/api/v1/site', admin)
  const [form, setForm] = useState(EMPTY_FORM)
  const [editing, setEditing] = useState<string>('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [caps, setCaps] = useState<Capabilities | null>(null)
  // Inline test feedback. The result used to be a transient notice; a capsule
  // that stays put lets the operator compare it against the form they edited.
  const [testState, setTestState] = useState<'idle' | 'running' | 'failed'>('idle')
  // What the failing step was and what the server answered, when it told us.
  const [testEvidence, setTestEvidence] = useState<{ stage: string; detail: string } | null>(null)
  const [showPassword, setShowPassword] = useState(false)
  const [pending, setPending] = useState(false)
  const [deleting, setDeleting] = useState<Target | null>(null)
  // The form starts collapsed. Editing or a failed probe opens it, because in
  // both cases the operator's next action is inside it — collapsing the very
  // field whose error we just reported would hide the fix.
  const [showForm, setShowForm] = useState(false)

  if (!admin)
    return <Empty>归档目标只对管理员开放。</Empty>

  function refresh() {
    void client.invalidateQueries()
  }

  async function run(action: () => Promise<void>, message: string) {
    setPending(true)
    setError('')
    setNotice('')
    try {
      await action()
      setNotice(message)
      refresh()
    } catch (cause) {
      setError(errorText(cause))
    } finally {
      setPending(false)
    }
  }

  function submit() {
    void run(async () => {
      if (editing) {
        await apiRequest(`/api/v1/archive-targets/${editing}`, jsonRequest('PATCH', form))
        setNotice('归档目标已更新')
      } else {
        await apiRequest('/api/v1/archive-targets', jsonRequest('POST', form))
      }
      setForm(EMPTY_FORM)
      setEditing('')
      setShowForm(false)
    }, editing ? '归档目标已更新' : '归档目标已登记')
  }

  async function probe(body: Record<string, unknown>) {
    setPending(true)
    setError('')
    setNotice('')
    setTestState('running')
    setCaps(null)
    setTestEvidence(null)
    try {
      const result = await apiRequest<Capabilities>('/api/v1/archive-targets/test', jsonRequest('POST', body))
      setCaps(result)
      setTestState('idle')
    } catch (cause) {
      setTestState('failed')
      setTestEvidence(evidenceOf(cause))
      setError(errorText(cause))
    } finally {
      setPending(false)
    }
  }

  function test() {
    void probe(editing ? { id: editing, ...form } : { ...form })
  }

  /** Probing a saved target needs no form state: the server has the credential. */
  function testTarget(target: Target) {
    void probe({ id: target.id })
  }

  /**
   * retryFailures reports its own count, so it cannot use the shared run()
   * helper: run() takes a fixed success message, and "已重新排队 N 项" carries a
   * number only the response knows.
   */
  async function retryFailures(targetID: string) {
    setPending(true)
    setError('')
    setNotice('')
    try {
      const result = await apiRequest<{ retried: number }>(
        `/api/v1/archive-targets/${targetID}/retry`,
        { method: 'POST' }
      )
      const count = result.retried ?? 0
      setNotice(count > 0 ? `已重新排队 ${count} 项上传` : '没有需要重试的项目')
      refresh()
    } catch (cause) {
      setError(errorText(cause))
    } finally {
      setPending(false)
    }
  }

  function edit(target: Target) {
    setShowForm(true)
    setEditing(target.id ?? '')
    setForm({
      name: target.name ?? '',
      base_url: target.base_url ?? '',
      remote_root: target.remote_root ?? '',
      username: target.username ?? '',
      password: '',
      cloud_retention_days: target.cloud_retention_days ?? 365,
    })
    setCaps(null)
  }

  return (
    <div className='space-y-4'>
      <div className='flex flex-wrap items-start justify-between gap-4'>
        <div>
          {/* h2, not h1: this panel sits under the storage workspace, whose tab
              strip is the page-level navigation. */}
          <h2 className='text-2xl font-semibold tracking-tight'>归档目标</h2>
          <p className='mt-1 text-sm text-muted-foreground'>
            WebDAV 归档目标是录像完成后的异步上传去处，与目录存储池互不替代。
          </p>
        </div>
        {/* Collapsed by default, like the storage-pool page: a registered target
            is what this page is usually opened to look at, and the form is a
            wide three-column block that pushed that content down the page. */}
        <Button
          type='button'
          variant={showForm ? 'outline' : 'default'}
          aria-expanded={showForm}
          onClick={() => setShowForm((value) => !value)}
        >
          <Plus aria-hidden className='size-4' />
          {showForm ? '收起表单' : editing ? '编辑目标' : '登记目标'}
        </Button>
      </div>
      <QueryState pending={targets.isPending} error={targets.error} retry={targets.refetch} />
      <Notices notice={notice} error={error} />
      <RunStatus
        targets={targets.data?.items || []}
        statuses={targetStatus.data?.items || []}
        pending={targetStatus.isPending}
        failed={!!targetStatus.error}
        error={targetStatus.error ? errorText(targetStatus.error) : ''}
        onRetry={(targetID) => void retryFailures(targetID)}
      />
      {showForm && (
      <form
        className='grid gap-3 rounded-xl border bg-card p-4'
        onSubmit={(event) => {
          event.preventDefault()
          submit()
        }}
      >
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <h3 className='text-sm font-medium'>
            {editing ? '编辑 WebDAV 目标' : '登记 WebDAV 目标'}
          </h3>
          <span className='rounded-full border border-emerald-500/20 bg-emerald-500/10 px-2 py-0.5 text-[11px] text-emerald-600 dark:text-emerald-400'>
            密码加密存储
          </span>
        </div>
        {/* items-start, not items-end: fields carry different amounts of
            helper text below the input (the URL shortcut, the retention hint),
            and bottom-alignment turns that difference into a visible stagger of
            both the labels and the inputs. Top-alignment keeps every label and
            every input on one line as long as each label row is one line — which
            is why the URL shortcut lives below its input. */}
        <div className='grid items-start gap-3 md:grid-cols-3'>
          <label className='grid gap-2 text-sm'>
            <span>名称</span>
            <Input
              aria-label='归档目标名称'
              value={form.name}
              onChange={(event) => setForm({ ...form, name: event.target.value })}
              required
              maxLength={64}
              placeholder='例如：NAS 归档 / OpenList 云盘备份'
            />
          </label>
          <label className='grid gap-2 text-sm'>
            <span>WebDAV URL</span>
            <Input
              aria-label='WebDAV URL'
              type='url'
              value={form.base_url}
              onChange={(event) => setForm({ ...form, base_url: event.target.value })}
              required
              placeholder='https://cloud.example.com/dav'
            />
            {/* The shortcut sits under the input rather than beside the label.
                Inside the label row it made that one row taller than every
                other, which pushed this input (and, with it, the whole grid)
                out of line with its neighbours.

                It is offered only when the bundled OpenList is actually there.
                The address it fills is a private service name that resolves to
                nothing unless that container ships, so an always-enabled button
                would hand the operator a URL that can only fail the probe. */}
            <BundledOpenListHint
              enabled={!!bundled?.enabled}
              state={bundled?.state ?? 'unknown'}
              consolePort={bundled?.ui_port ?? 0}
              onFill={() => setForm({ ...form, base_url: OPENLIST_DAV })}
            />
          </label>
          <label className='grid gap-2 text-sm'>
            <span>远端根目录</span>
            <Input
              aria-label='远端根目录'
              value={form.remote_root}
              onChange={(event) => setForm({ ...form, remote_root: event.target.value })}
              required
              placeholder='/one-nvr'
            />
          </label>
          <label className='grid gap-2 text-sm'>
            <span>账号</span>
            <Input
              aria-label='WebDAV 账号'
              value={form.username}
              onChange={(event) => setForm({ ...form, username: event.target.value })}
              required
              autoComplete='off'
            />
          </label>
          <label className='grid gap-2 text-sm'>
            <span>密码{editing ? '（留空保持不变）' : ''}</span>
            <div className='flex'>
              <Input
                aria-label='WebDAV 密码'
                type={showPassword ? 'text' : 'password'}
                value={form.password}
                onChange={(event) => setForm({ ...form, password: event.target.value })}
                required={!editing}
                autoComplete='new-password'
                className='rounded-r-none'
              />
              {/* WebDAV application passwords are long and easy to mistype; a
                  reveal toggle is the difference between verifying and guessing.

                  size='icon' (h-9, matching Input) rather than 'sm' (h-8): the
                  two are joined into one control by removing the facing borders,
                  so a 4px height difference shows up as a step in the outline. */}
              <Button
                type='button'
                variant='outline'
                size='icon'
                aria-label={showPassword ? '隐藏密码' : '显示密码'}
                aria-pressed={showPassword}
                className='rounded-l-none border-l-0 px-2'
                onClick={() => setShowPassword((value) => !value)}
              >
                {showPassword ? <EyeOff aria-hidden className='size-4' /> : <Eye aria-hidden className='size-4' />}
              </Button>
            </div>
          </label>
          <label className='grid gap-2 text-sm'>
            <span>云端保留</span>
            <div className='relative'>
              <Input
                aria-label='云端保留天数'
                type='number'
                min={0}
                max={36500}
                value={form.cloud_retention_days}
                onChange={(event) => setForm({ ...form, cloud_retention_days: Number(event.target.value || 0) })}
                required
                className='pr-8'
              />
              <span className='pointer-events-none absolute inset-y-0 right-2.5 flex items-center text-xs text-muted-foreground'>
                天
              </span>
            </div>
            <span className='text-[11px] text-muted-foreground'>默认 365 天，填 0 为永久保留</span>
          </label>
        </div>
        <div className='flex flex-wrap items-center gap-2'>
          <Button type='button' variant='outline' disabled={pending} onClick={test}>
            测试连接
          </Button>
          <Button type='submit' disabled={pending}>
            {editing ? '保存修改' : '登记目标'}
          </Button>
          {editing && (
            <Button
              type='button'
              variant='ghost'
              onClick={() => {
                setEditing('')
                setForm(EMPTY_FORM)
                setCaps(null)
              }}
            >
              取消编辑
            </Button>
          )}
          <CapabilityBadges caps={caps} state={testState} evidence={testEvidence} />
        </div>
      </form>
      )}
      {/* Order is deliberate: what is already configured comes before adding
          more, and the per-channel policy table comes last. The table is the
          longest block on the page (one row per channel, 16+ rows), so putting
          anything after it buries that content below a screenful of dropdowns. */}
      {!targets.data?.items.length && !targets.isPending ? (
        /* A compact empty state: the full Empty block is a 64px dashed box, and
           on a fresh install this is the normal state of the page. */
        <p className='rounded-lg border border-dashed px-3 py-3.5 text-center text-sm text-muted-foreground'>
          还没有归档目标。登记一个 WebDAV 目标后，可在录像管理页对已发布录像发起上传。
        </p>
      ) : (
        <div className='space-y-3'>
          {(targets.data?.items || []).map((target) => (
            <div
              key={target.id}
              className='rounded-xl border bg-card p-4 text-sm'
              data-testid='archive-target-row'
            >
              <div className='flex flex-wrap items-start justify-between gap-2'>
                <div className='min-w-0'>
                  <p className='flex flex-wrap items-center gap-2 font-medium'>
                    {target.name}
                    <span
                      className={cn(
                        'rounded-full border px-1.5 py-0.5 text-[11px] font-normal',
                        target.enabled
                          ? 'border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
                          : 'border-dashed text-muted-foreground'
                      )}
                    >
                      {target.enabled ? '已启用' : '已停用'}
                    </span>
                  </p>
                  <p className='mt-1 truncate font-mono text-xs text-muted-foreground'>
                    {target.base_url}
                    {target.remote_root}
                  </p>
                </div>
                <div className='flex flex-wrap items-center gap-2'>
                  <Button size='sm' variant='outline' disabled={pending} onClick={() => void testTarget(target)}>
                    测试
                  </Button>
                  <Button size='sm' variant='outline' onClick={() => edit(target)}>
                    编辑
                  </Button>
                  <Button
                    size='sm'
                    variant='ghost'
                    onClick={() =>
                      void run(async () => {
                        await apiRequest(`/api/v1/archive-targets/${target.id}`, jsonRequest('PATCH', { ...target, password: '', enabled: !target.enabled }))
                      }, target.enabled ? '目标已停用' : '目标已启用')
                    }
                  >
                    {target.enabled ? '停用' : '启用'}
                  </Button>
                  <Button
                    size='sm'
                    variant='ghost'
                    className='text-destructive'
                    onClick={() => setDeleting(target)}
                  >
                    删除
                  </Button>
                </div>
              </div>
              <dl className='mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground'>
                <div className='flex items-center gap-1'>
                  <dt>保留</dt>
                  <dd className='tabular-nums text-foreground'>
                    {(target.cloud_retention_days ?? 0) > 0
                      ? `${target.cloud_retention_days} 天`
                      : '永久'}
                  </dd>
                </div>
                <div className='flex items-center gap-1'>
                  <dt>账号</dt>
                  <dd className='text-foreground'>{target.username}</dd>
                </div>
                <div className='flex items-center gap-1'>
                  <dt>最近测试</dt>
                  <dd>
                    <StoredCheck check={target.last_check} />
                  </dd>
                </div>
              </dl>
            </div>
          ))}
        </div>
      )}
      {/* The longest block on the page sits last, after the target list, so
          nothing useful is buried under 16 rows of dropdowns. Both are about
          the same targets from different sides: this one asks "did it arrive",
          the table asks "where does this channel go". */}
      {admin && !!targets.data?.items.length && (
        <ArchivePlan
          channels={channels.data?.items || []}
          targets={targets.data?.items || []}
          policies={policies.data?.items || []}
          statuses={channelStatus.data?.items || []}
          zone={site.data?.timezone || ''}
        />
      )}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(next) => !next && setDeleting(null)}
        title={`删除归档目标「${deleting?.name ?? ''}」？`}
        desc='目标登记与凭据将被移除；已有云端副本不受影响。有归档副本的目标会被服务端拒绝，请先停用。'
        confirmText='删除'
        cancelBtnText='取消'
        destructive
        isLoading={pending}
        handleConfirm={() => {
          const target = deleting
          if (!target) return
          setDeleting(null)
          void run(async () => {
            await apiRequest(`/api/v1/archive-targets/${target.id}`, { method: 'DELETE' })
          }, '目标已删除')
        }}
      />
    </div>
  )
}

/**
 * ArchivePlan is the page's single configuration surface, plus a read-only index
 * of what each channel ended up with.
 *
 * Same shape as the recording-plan page, for the same reason: a sixteen-row
 * table where every row carries a target select, a mode select and a time box is
 * sixteen places to change one setting, and the operator has to work out per row
 * which of them they are editing. The bar states the binding once and applies it
 * to whatever is selected; the table only reports the result.
 *
 * Rows are selectable and clicking anywhere on one toggles it, because selecting
 * is the only thing a row still does.
 */
function ArchivePlan({
  channels,
  targets,
  policies,
  statuses,
  zone,
}: {
  channels: Schema<'Channel'>[]
  targets: Target[]
  policies: Policy[]
  statuses: ChannelStatus[]
  zone: string
}) {
  const client = useQueryClient()
  const enabled = targets.filter((target) => target.enabled)
  const byChannel = new Map(policies.map((policy) => [policy.channel_id ?? '', policy]))
  const statusByChannel = new Map(statuses.map((item) => [item.channel_id ?? '', item]))
  const [selected, setSelected] = useState<Record<string, boolean>>({})
  // Drafts are null until the operator touches the bar. Selecting a single
  // channel makes the bar echo that channel's saved binding; the echo is a
  // fallback, never a write, so it cannot clobber a value the operator just set
  // (the bug that made this page's event switch look dead once before).
  const [draftTarget, setDraftTarget] = useState<string | null>(null)
  const [draftMode, setDraftMode] = useState<'manual' | 'immediate' | 'scheduled' | null>(null)
  const [draftSchedule, setDraftSchedule] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [pending, setPending] = useState(false)

  const chosen = channels.filter((channel) => selected[channel.id ?? ''])
  const chosenCount = chosen.length

  // Echo a single selected channel's saved binding. With more than one selected
  // there is no single current value, so the bar keeps what the operator set — a
  // mixed selection has no state to echo, and showing the first channel's would
  // be a lie they could act on.
  const solo = chosenCount === 1 ? chosen[0] : undefined
  const soloPolicy = solo ? byChannel.get(solo.id ?? '') : undefined
  const targetID = draftTarget ?? soloPolicy?.target_id ?? enabled[0]?.id ?? ''
  const mode = draftMode ?? (soloPolicy?.mode as NonNullable<typeof draftMode>) ?? 'manual'
  const schedule = draftSchedule ?? soloPolicy?.schedule ?? ''

  function toggleAll() {
    if (chosenCount === channels.length) {
      setSelected({})
      return
    }
    setSelected(Object.fromEntries(channels.map((channel) => [channel.id ?? '', true])))
  }

  async function apply() {
    if (!chosenCount) {
      setError('请先选择通道')
      return
    }
    if (!targetID) {
      setError('请选择归档目标')
      return
    }
    // The server rejects this with "参数无效", which does not name the field.
    if (mode === 'scheduled' && !schedule.trim()) {
      setError('定时上传需要填写时间段，例如 02:00-06:00（该时段内持续上传）。')
      return
    }
    setPending(true)
    setError('')
    setNotice('')
    let done = 0
    try {
      // Sequential, not parallel: each write advances the channel's policy row,
      // and concurrent writes to the same table fight over it. It also keeps the
      // audit trail in the order the operator selected.
      for (const channel of chosen) {
        await apiRequest(
          '/api/v1/archive-policies',
          jsonRequest('PUT', {
            channel_id: channel.id ?? '',
            target_id: targetID,
            mode,
            schedule: mode === 'scheduled' ? schedule.trim() : '',
          })
        )
        done++
      }
      setNotice(`${done} 路已应用`)
      await client.invalidateQueries()
    } catch (cause) {
      // Partial success is reported as such: the operator needs to know which
      // channels still hold the old binding.
      setError(done > 0 ? `已应用 ${done} 路，其余失败：${errorText(cause)}` : errorText(cause))
      await client.invalidateQueries()
    } finally {
      setPending(false)
    }
  }

  return (
    <div className='space-y-3 rounded-xl border bg-card p-4'>
      <div>
        <h2 className='text-sm font-medium'>通道归档策略</h2>
        <p className='mt-1 text-xs text-muted-foreground'>
          勾选通道后在下方设置一次并应用，表格只展示当前结果。实时上传＝持续把未归档的录像传上去（含历史积压），不受时段限制；定时上传＝只在指定时段内上传，时段外排队等待；手动＝只在录像管理页选中后上传。时段按站点时区
          {zone ? `（${zone}）` : ''}计算。
        </p>
      </div>
      {/* The single configuration surface. */}
      <div className='flex flex-wrap items-end gap-2 rounded-lg border bg-muted/30 p-3'>
        <div className='text-xs text-muted-foreground'>
          已选 <span className='tabular-nums text-foreground'>{chosenCount}</span> / {channels.length} 路
        </div>
        <Button size='sm' variant='outline' onClick={() => setSelected({})} disabled={!chosenCount}>
          清空
        </Button>
        <SelectField
          ariaLabel='归档目标'
          value={targetID}
          onValueChange={(next) => setDraftTarget(next ?? '')}
          options={enabled.map((target) => ({ value: target.id ?? '', label: target.name ?? '' }))}
          triggerClassName='h-8 w-40'
        />
        <SelectField
          ariaLabel='上传模式'
          value={mode}
          onValueChange={(next) => setDraftMode((next ?? 'manual') as NonNullable<typeof draftMode>)}
          options={[
            { value: 'manual', label: '手动' },
            { value: 'immediate', label: '实时上传' },
            { value: 'scheduled', label: '定时上传' },
          ]}
          triggerClassName='h-8 w-32'
        />
        {mode === 'scheduled' && (
          <Input
            aria-label='上传时间段'
            value={schedule}
            onChange={(event) => setDraftSchedule(event.target.value)}
            placeholder='02:00-06:00'
            className='h-8 w-44'
          />
        )}
        <Button size='sm' disabled={pending || !chosenCount} onClick={() => void apply()}>
          {pending ? '应用中…' : `应用到 ${chosenCount || 0} 路`}
        </Button>
      </div>
      {error && (
        <p role='alert' className='text-sm text-destructive'>
          {error}
        </p>
      )}
      {notice && <p className='text-sm text-emerald-600 dark:text-emerald-400'>{notice}</p>}
      <div className='overflow-x-auto'>
        <table className='w-full text-sm'>
          <thead>
            <tr className='text-left text-xs text-muted-foreground'>
              <th className='w-8 pb-2 pr-2 font-normal'>
                <Checkbox
                  aria-label='全选通道'
                  checked={channels.length > 0 && chosenCount === channels.length}
                  onCheckedChange={toggleAll}
                />
              </th>
              <th className='pb-2 pr-3 font-normal'>通道</th>
              <th className='pb-2 pr-3 font-normal'>归档目标</th>
              <th className='pb-2 pr-3 font-normal'>上传模式</th>
              <th className='pb-2 pr-3 font-normal'>已归档</th>
              <th className='pb-2 font-normal'>失败</th>
            </tr>
          </thead>
          <tbody>
            {channels.map((channel) => {
              const id = channel.id ?? ''
              const label = `CH${String(channel.channel_no ?? 0).padStart(2, '0')} ${channel.channel_name}`
              const policy = byChannel.get(id)
              const status = statusByChannel.get(id)
              // Only a bound channel has anything to cover. An unbound channel
              // holding 364 local segments is not "0 of 364 archived" — nothing
              // was ever asked for, and showing 0/N reads as a failing archive.
              const cover = policy ? coverage(status?.copied, status?.segments) : null
              const isSelected = !!selected[id]
              return (
                // The whole row selects: with the controls gone, selecting is the
                // only thing a row does.
                <tr
                  key={id}
                  className={cn('cursor-pointer border-t', isSelected && 'bg-accent/40')}
                  onClick={() => setSelected({ ...selected, [id]: !isSelected })}
                  data-testid='archive-policy-row'
                >
                  <td className='py-1.5 pr-2' onClick={(event) => event.stopPropagation()}>
                    <Checkbox
                      aria-label={`${label} 选择`}
                      checked={isSelected}
                      onCheckedChange={(next) => setSelected({ ...selected, [id]: !!next })}
                    />
                  </td>
                  <td className='py-1.5 pr-3 whitespace-nowrap'>{label}</td>
                  <td className='py-1.5 pr-3'>
                    {policy ? (
                      <span>
                        {status?.target_name || '—'}
                        {status && !status.target_enabled && (
                          <span className='ml-1 rounded-full border border-dashed px-1.5 py-0.5 text-[11px] text-muted-foreground'>
                            已停用
                          </span>
                        )}
                      </span>
                    ) : (
                      <span className='text-xs text-muted-foreground'>未归档</span>
                    )}
                  </td>
                  <td className='py-1.5 pr-3'>
                    {policy ? (
                      <span>
                        {MODE_LABEL[policy.mode ?? ''] ?? policy.mode}
                        {policy.mode === 'scheduled' && policy.schedule && (
                          <span className='ml-1 font-mono text-xs text-muted-foreground'>{policy.schedule}</span>
                        )}
                      </span>
                    ) : (
                      <span className='text-xs text-muted-foreground'>—</span>
                    )}
                  </td>
                  <td className='py-1.5 pr-3'>
                    {cover ? (
                      <span className='tabular-nums text-xs'>
                        {cover.copied}/{cover.segments}
                      </span>
                    ) : (status?.segments ?? 0) > 0 ? (
                      // Footage exists locally but was never asked to archive.
                      // It is shown, not hidden: "this channel holds 364
                      // segments and none of them are archived" is a fact the
                      // operator needs, while a row of "—" reads as "nothing
                      // here".
                      <span className='text-xs text-muted-foreground'>
                        本机 <span className='tabular-nums text-foreground'>{status?.segments}</span> 段 · 未归档
                      </span>
                    ) : (
                      <span className='text-xs text-muted-foreground'>—</span>
                    )}
                  </td>
                  <td className='py-1.5'>
                    {(status?.failed ?? 0) > 0 ? (
                      <span className='text-xs text-red-500'>{status?.failed}</span>
                    ) : (
                      <span className='text-xs text-muted-foreground'>—</span>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}

/** The bundled OpenList container, reachable on the private network. */
const OPENLIST_DAV = 'http://openlist:5244/dav'

/**
 * BundledOpenListHint offers the bundled container's address, but only when
 * there is a container to reach.
 *
 * Three states, because "off" and "broken" need different words and only one of
 * them is something the operator can act on here:
 *
 * - the module is off → say so and leave the field to be filled by hand. The
 *   private address resolves to nothing in that deployment.
 * - on but not healthy → the button stays visible but disabled, naming the
 *   reason. Filling a dead address produces a confusing probe failure later,
 *   which is exactly what this shortcut was added to avoid.
 * - on and healthy → the one-click fill.
 *
 * An unobserved component (state 'unknown', or no row yet) is treated as
 * unavailable: "we have not proven it works" is not "it works", and the cost of
 * being wrong here is a target that can never be reached.
 */
function BundledOpenListHint({
  enabled,
  state,
  consolePort,
  onFill,
}: {
  enabled: boolean
  state: string
  consolePort: number
  onFill: () => void
}) {
  if (!enabled || state === 'disabled') {
    return (
      <span className='text-[11px] text-muted-foreground'>
        未启用自带的 OpenList（部署时 <code>ONE_NVR_OPENLIST_ENABLE=yes</code> 开启）。请手填你自建的 WebDAV 地址。
      </span>
    )
  }
  const healthy = state === 'healthy'
  const consoleURL = bundledConsoleUrl(window.location.hostname, consolePort)
  return (
    <span className='flex flex-wrap items-center gap-2'>
      <button
        type='button'
        aria-label='填入 OpenList 默认地址'
        disabled={!healthy}
        title={healthy ? '填入自带 OpenList 的 WebDAV 地址' : '自带 OpenList 当前不可用'}
        className='w-fit rounded border px-1.5 py-0.5 text-[11px] font-normal text-muted-foreground hover:bg-accent hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent disabled:hover:text-muted-foreground'
        onClick={(event) => {
          // The button lives inside the field's <label>: without this the click
          // also focuses the input it belongs to.
          event.preventDefault()
          onFill()
        }}
      >
        填入 OpenList 默认地址
      </button>
      {/* The console link is separate from the fill button and does not depend on
          the container answering: the very first thing an operator has to do is
          create the administrator account in that console, and before that
          happens the probe may legitimately report unavailable. Hiding the way in
          would leave a fresh deployment with no path forward. */}
      {consoleURL ? (
        <a
          href={consoleURL}
          target='_blank'
          rel='noreferrer'
          className='w-fit rounded border px-1.5 py-0.5 text-[11px] font-normal text-muted-foreground hover:bg-accent hover:text-foreground'
        >
          打开 OpenList 配置页
        </a>
      ) : (
        <span className='text-[11px] text-muted-foreground'>
          配置页未发布（设置 <code>ONE_NVR_OPENLIST_UI_PORT</code> 后重新部署即可从这里打开）
        </span>
      )}
      {!healthy && (
        <span className='text-[11px] text-amber-600 dark:text-amber-400'>
          自带 OpenList {state === 'unknown' ? '状态未知' : '不可达'}，请先在运维页确认
        </span>
      )}
    </span>
  )
}

/**
 * CapabilityBadges renders the probe result inline, next to the buttons.
 *
 * ARC-01's promise is that a probe verifies write, read, range and delete. A
 * green tick with no detail would be a claim; showing the four checks
 * individually means a target that cannot DELETE (and therefore cannot support
 * cloud expiry) is visibly incomplete rather than quietly "OK".
 */
/**
 * evidenceOf pulls the failing stage out of an ApiError.
 *
 * The server reports it as fields.stage / fields.detail, so the badge can say
 * "写入失败 · HTTP 409" next to the sentence explaining what to change. A code
 * with no evidence simply yields null and the badge stays terse.
 */
function evidenceOf(error: unknown): { stage: string; detail: string } | null {
  if (!(error instanceof ApiError)) return null
  const stage = error.fields?.stage ?? ''
  const detail = error.fields?.detail ?? ''
  if (!stage && !detail) return null
  return { stage, detail }
}

const STAGE_LABEL: Record<string, string> = {
  connect: '连接地址',
  'create-collection': '创建目录',
  write: '写入',
  read: '读取',
  'read-back': '回读校验',
  delete: '删除',
}

function CapabilityBadges({
  caps,
  state,
  evidence,
}: {
  caps: Capabilities | null
  state: 'idle' | 'running' | 'failed'
  evidence: { stage: string; detail: string } | null
}) {
  if (state === 'running') {
    return <span className='text-xs text-muted-foreground'>测试中…</span>
  }
  if (state === 'failed') {
    // "连接失败" alone hides whether the address, the credentials or the root
    // path was wrong; the stage is what makes the message actionable.
    const stage = evidence ? (STAGE_LABEL[evidence.stage] ?? evidence.stage) : ''
    return (
      <span
        role='status'
        className='rounded-full border border-red-500/30 bg-red-500/10 px-2 py-0.5 text-xs text-red-500'
      >
        测试失败{stage ? ` · ${stage}` : ''}
        {evidence?.detail ? ` · ${evidence.detail}` : ''}
      </span>
    )
  }
  if (!caps) return null
  const checks: [string, boolean, string][] = [
    ['写入', !!caps.write, '无法写入'],
    ['读取', !!caps.read, '无法读取'],
    ['Range', !!caps.range, '不支持断点续传'],
    ['删除', !!caps.delete, '到期清理不可用'],
  ]
  const all = checks.every(([, ok]) => ok)
  return (
    <span
      role='status'
      className={cn(
        'flex flex-wrap items-center gap-1 rounded-full border px-2 py-0.5 text-xs',
        all
          ? 'border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
          : 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400'
      )}
    >
      {all ? '✓ 连接正常（' : '⚠ 能力不完整（'}
      {checks.map(([label, ok, why], index) => (
        <span key={label} title={ok ? `${label}验证通过` : why}>
          {index > 0 && ' · '}
          {label}
          {ok ? '✓' : '✗'}
        </span>
      ))}
      ）
    </span>
  )
}

/**
 * StoredCheck renders the probe recorded on the target row.
 *
 * It is evidence from the last test, notLive state: a target can be enabled while
 * its last probe failed, and showing that difference is the point.
 */
function StoredCheck({ check }: { check: unknown }) {
  const value = check as
    | { state?: string; capabilities?: Capabilities; observed_at?: string }
    | null
    | undefined
  if (!value || !value.state) {
    return <span className='text-muted-foreground'>未测试</span>
  }
  const capabilities = value.capabilities
  const complete =
    capabilities &&
    capabilities.write &&
    capabilities.read &&
    capabilities.range &&
    capabilities.delete
  return (
    <span
      className={cn(
        complete
          ? 'text-emerald-600 dark:text-emerald-400'
          : 'text-amber-600 dark:text-amber-400'
      )}
      title={value.observed_at ? new Date(value.observed_at).toLocaleString('zh-CN', { hour12: false }) : undefined}
    >
      {value.state === 'healthy' ? (complete ? '正常' : '能力不完整') : '不可达'}
    </span>
  )
}
