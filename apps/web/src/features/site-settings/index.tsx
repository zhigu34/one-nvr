import { useEffect, useState } from 'react'
import { useAuthStore } from '@/stores/auth-store'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema, Capabilities } from '@/lib/types'
import { Button } from '@/components/ui/button'
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
import { TLSSettings } from '@/features/tls-settings'

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
  const action = useAction()
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
                onClick={() => {
                  if (
                    window.confirm(
                      '追加 CH17–CH32？现有通道身份保持，扩容不能缩回。'
                    )
                  )
                    void action.run(
                      () =>
                        apiRequest(
                          '/api/v1/site/expand',
                          jsonRequest('POST', undefined, query.data!.version)
                        ),
                      '已扩容至 32 路'
                    )
                }}
              >
                扩容至 32 路
              </Button>
            )}
          </div>
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
    </>
  )
}
// Storage write proof is the one reliability knob a site may trade away: with
// it off, binding a pool or enabling continuous recording no longer waits for a
// verified MP4 write. The runtime capacity gate and the watchdog that stops a
// recorder producing nothing stay active either way, so the panel states the
// cost in one sentence rather than hiding it behind a bare label.
function StorageProofSetting({ site }: { site: Schema<'Site'> }) {
  const action = useAction()
  return (
    <div className='mt-4 rounded-lg border p-4'>
      <div className='flex flex-wrap items-start justify-between gap-3'>
        <div className='min-w-0'>
          <p className='text-sm font-medium'>存储写入检查</p>
          <p className='mt-1 text-sm text-muted-foreground'>
            {site.require_storage_write_proof
              ? '已开启：绑定存储池或开启连续录像前，先真实写入并校验一段录像，确保录像能落盘。'
              : '已关闭：不再写入校验，绑定与开启连续录像立即生效；若存储实际写不进去，最多约 3 分钟后停止录像并在通道上报告。'}
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
function ModuleStatus({
  name,
  value,
  testId,
}: {
  name: string
  value: Schema<'Capability'>
  testId: string
}) {
  return (
    <div data-testid={testId} className='rounded-lg border p-4'>
      <h3 className='font-medium'>{name}</h3>
      <p className='mt-2 text-sm'>
        {!value.enabled
          ? '功能未启用'
          : !value.implemented
            ? '尚未实现'
            : '已实现'}
      </p>
      <p className='mt-2 text-xs text-muted-foreground'>{value.reason}</p>
    </div>
  )
}
