import { useEffect, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema, Capabilities } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { useAPI, useAction, fields, text } from '@/features/foundation/hooks'
import {
  PageTitle,
  Panel,
  Field,
  SelectField,
  QueryState,
  Notices,
  Forbidden,
} from '@/features/foundation/ui'
import { StatusBadge, type StatusTone } from '@/features/foundation/status-badge'
import { TLSSettings } from '@/features/tls-settings'
import { NotificationChannels } from '@/features/notification-channels'

type Timezone = { id: string; label: string; current_time: string }
export function Settings() {
  const admin = useAuthStore((s) => s.user?.role === 'admin')
  const query = useAPI<Schema<'Site'>>('/api/v1/site', admin)
  const zones = useAPI<Timezone[]>('/api/v1/timezones', admin)
  const modules = useAPI<Capabilities>('/api/v1/capabilities', admin, 5000)
  const hardware = useAPI<Schema<'HardwareReport'>>(
    '/api/v1/settings/hardware',
    admin && !!modules.data?.frigate.enabled,
    5000
  )
  const action = useAction(true)
  const [confirmExpand, setConfirmExpand] = useState(false)
  if (!admin) return <Forbidden />
  return (
    <>
      <PageTitle
        title='系统设置'
        description='站点、时区、模块状态和访问证书。模块启停由部署配置控制。'
      />
      <QueryState
        pending={query.isPending || zones.isPending}
        error={query.error || zones.error}
        retry={() => {
          void query.refetch()
          void zones.refetch()
        }}
      />
      {query.data && zones.data && (
        <Panel title='站点与时区'>
          <Notices {...action} />
          <form
            key={query.data.version}
            className='grid gap-4 md:grid-cols-3'
            onSubmit={(event) => {
              event.preventDefault()
              const data = fields(event.currentTarget)
              void action.run(
                () =>
                  apiRequest(
                    '/api/v1/site',
                    jsonRequest(
                      'PATCH',
                      {
                        name: text(data, 'name'),
                        timezone: text(data, 'timezone'),
                      },
                      query.data!.version
                    )
                  ),
                '站点设置已保存'
              )
            }}
          >
            <Field
              label='站点名称'
              name='name'
              defaultValue={query.data.name}
              maxLength={128}
              required
            />
            <SelectField
              label='时区'
              name='timezone'
              defaultValue={query.data.timezone}
              options={zones.data.map((z) => ({
                value: z.id,
                label: `${z.label} · ${z.id}`,
              }))}
            />
            <Button className='self-end' disabled={action.pending}>
              保存站点设置
            </Button>
          </form>
          <SiteClock zone={query.data.timezone} />
          <StorageProofSetting site={query.data} />
          <div className='mt-4 flex items-center gap-4'>
            <p className='text-sm text-muted-foreground'>
              固定槽位 {query.data.channel_count} 路
            </p>
            {query.data.channel_count === 16 && (
              <Button
                variant='outline'
                disabled={action.pending}
                onClick={() => setConfirmExpand(true)}
              >
                扩容至 32 路
              </Button>
            )}
          </div>
          <ConfirmDialog
            open={confirmExpand}
            onOpenChange={setConfirmExpand}
            title='追加 CH17–CH32？'
            desc='现有通道身份保持，扩容不能缩回。'
            confirmText='扩容至 32 路'
            cancelBtnText='取消'
            isLoading={action.pending}
            handleConfirm={() =>
              void action.run(
                () =>
                  apiRequest(
                    '/api/v1/site/expand',
                    jsonRequest('POST', undefined, query.data!.version)
                  ),
                '已扩容至 32 路'
              )
            }
          />
        </Panel>
      )}
      <Panel title='功能模块'>
        <QueryState
          pending={modules.isPending}
          error={modules.error}
          retry={() => modules.refetch()}
        />
        {modules.data && (
          <div className='grid gap-4 md:grid-cols-2'>
            <ModuleStatus
              name='智能检测'
              testId='intelligence-status'
              value={modules.data.frigate}
            />
            <ModuleStatus
              name='云归档'
              testId='cloud-status'
              value={modules.data.cloud_archive}
            />
          </div>
        )}
      </Panel>
      {modules.data?.frigate.enabled && (
        <Panel title='智能检测硬件'>
          <QueryState
            pending={hardware.isPending}
            error={hardware.error}
            retry={() => hardware.refetch()}
          />
          {hardware.data && (
            <>
              <p>
                自检：{hardware.data.validated ? '通过' : '失败'} ·{' '}
                {hardware.data.error_code || 'validated'}
              </p>
              <p>
                解码：{hardware.data.proposal.decode_id} · 推理：
                {hardware.data.proposal.inference_id}
              </p>
              <p className='text-sm text-muted-foreground'>
                设备：{hardware.data.proposal.node || 'CPU'} ·
                样本自检不代表摄像头业务或容量验收。
              </p>
            </>
          )}
        </Panel>
      )}
      <TLSSettings />
      <Panel title='通知渠道'>
        <NotificationChannels />
      </Panel>
    </>
  )
}
// Storage write proof is the one reliability knob a site may trade away: with
// it off, binding a pool or enabling continuous recording no longer waits for a
// verified MP4 write. The runtime capacity gate and the watchdog that stops a
// recorder producing nothing stay active either way, so the panel states the
// cost in one sentence rather than hiding it behind a bare label.
function StorageProofSetting({ site }: { site: Schema<'Site'> }) {
  const action = useAction(true)
  return (
    <div className='mt-4 rounded-lg border p-4'>
      <div className='flex flex-wrap items-start justify-between gap-3'>
        <div className='min-w-0'>
          <p className='text-sm font-medium'>存储写入检查</p>
          <p className='mt-1 text-sm text-muted-foreground'>
            {site.require_storage_write_proof
              ? '已开启：绑定存储池或开启全天录像前，先真实写入并校验一段录像，确保录像能落盘。'
              : '已关闭：不再写入校验，绑定与开启全天录像立即生效；若存储实际写不进去，最多约 3 分钟后停止录像并在通道上报告。'}
          </p>
          <p className='mt-1 text-xs text-muted-foreground'>
            两种情况都会继续检查磁盘容量，写满时照常停止录像。
          </p>
        </div>
        <Button
          type='button'
          variant='outline'
          disabled={action.pending}
          onClick={() =>
            void action.run(
              () =>
                apiRequest(
                  '/api/v1/site',
                  jsonRequest(
                    'PATCH',
                    {
                      require_storage_write_proof:
                        !site.require_storage_write_proof,
                    },
                    site.version
                  )
                ),
              site.require_storage_write_proof
                ? '已关闭存储写入检查'
                : '已开启存储写入检查'
            )
          }
        >
          {site.require_storage_write_proof ? '关闭写入检查' : '开启写入检查'}
        </Button>
      </div>
      <Notices {...action} />
    </div>
  )
}
function SiteClock({ zone }: { zone: string }) {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(timer)
  }, [])
  return (
    <p data-testid='site-clock' className='mt-4 text-sm text-muted-foreground'>
      当前站点时间：
      {now.toLocaleString('zh-CN', { timeZone: zone, hour12: false })} · {zone}
    </p>
  )
}
// The PRD's four operator-facing states, plus what a disabled module says. The
// vocabulary matters: "运行异常" must never be worded as "尚未实现" — one is a
// broken dependency the operator can look at, the other claims the product
// lacks the feature. Each state also says where to go next, because "已启用未
// 配置" is only useful if it points at the page that configures it.
const MODULE_STATES: Record<
  string,
  { label: string; tone: StatusTone; hint: string }
> = {
  disabled: {
    label: '功能未启用',
    tone: 'neutral',
    hint: '该模块未随本次部署启用（.env 中的开关为 no）。',
  },
  not_configured: {
    label: '待配置',
    tone: 'warning',
    hint: '模块已启用，但还没有可用的配置。',
  },
  healthy: {
    label: '运行中',
    tone: 'success',
    hint: '已配置，且依赖组件响应正常。',
  },
  unavailable: {
    label: '运行异常',
    tone: 'destructive',
    hint: '已配置，但依赖组件没有响应，请到运维与审计页查看组件状态。',
  },
  // Configured, never tested. Saying 运行异常 here would send the operator after
  // a fault that has not happened — measured on this site, uploads were succeeding
  // through a target whose card claimed it was broken.
  unverified: {
    label: '未验证',
    tone: 'warning',
    hint: '已配置，但还没有测试过。到归档目标页点一次「测试」即可确认。',
  },
  not_available: {
    label: '版本尚未提供',
    tone: 'info',
    hint: '当前版本不包含该模块。',
  },
}

/** Where a module is configured, so a pending state is actionable. */
const MODULE_LINKS: Record<
  string,
  { to: string; label: string; search?: Record<string, string> }
> = {
  '智能检测': { to: '/detection', label: '去配置检测' },
  // Archive targets are a tab of the storage workspace now, so the link has to
  // land on that tab rather than on the pool list.
  '云归档': {
    to: '/storage',
    label: '去配置归档目标',
    search: { tab: 'archive' },
  },
}

function ModuleStatus({
  name,
  value,
  testId,
}: {
  name: string
  value: Schema<'Capability'>
  testId: string
}) {
  const state = MODULE_STATES[value.state ?? ''] || MODULE_STATES.unavailable
  const link = MODULE_LINKS[name]
  return (
    <div data-testid={testId} className='rounded-lg border p-4'>
      <div className='flex items-center justify-between gap-2'>
        <h3 className='font-medium'>{name}</h3>
        <StatusBadge tone={state.tone}>{state.label}</StatusBadge>
      </div>
      <p className='mt-2 text-xs text-muted-foreground'>{state.hint}</p>
      {value.state === 'not_configured' && link && (
        <Button asChild size='sm' variant='outline' className='mt-2'>
          <Link to={link.to} search={link.search ?? {}}>
            {link.label}
          </Link>
        </Button>
      )}
      {/* The raw reason code is a support handle, not an explanation: the hint
          above carries the meaning, and an operator who needs to check the
          exact code against the logs still can. */}
      <p className='mt-2 text-xs text-muted-foreground/70'>{value.reason}</p>
    </div>
  )
}
