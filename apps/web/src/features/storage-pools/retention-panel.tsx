import { useState } from 'react'
import { apiRequest, jsonRequest } from '@/lib/api-client'
import type { Schema } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { useAPI, useAction } from '@/features/foundation/hooks'
import { Notices, SelectField } from '@/features/foundation/ui'

/**
 * The site's local recording retention.
 *
 * It lives on the storage page because that is where an operator decides how
 * long footage is kept, but its scope is the SITE, not a pool: one period covers
 * every channel, whichever pool it is bound to. The wording says so explicitly —
 * the panel used to sit under the recording list, where it read as a property of
 * the listing (or, once pool binding moved here, of one pool).
 *
 * Retention deletes footage while nobody is watching, so the panel states what
 * the period would remove before it is enabled, and "off" is the default.
 */
export function RetentionPolicyPanel() {
  const site = useAPI<Schema<'Site'>>('/api/v1/site', true)
  if (site.isPending) return <p className='text-sm text-muted-foreground'>读取中…</p>
  if (site.error) {
    return (
      <Notices
        {...{ error: site.error instanceof Error ? site.error.message : '读取失败', notice: '' }}
      />
    )
  }
  if (!site.data) return null
  return <RetentionPolicyForm site={site.data} onSaved={() => void site.refetch()} />
}

function RetentionPolicyForm({
  site,
  onSaved,
}: {
  site: Schema<'Site'>
  onSaved: () => void
}) {
  const action = useAction()
  const [days, setDays] = useState(String(site.recording_retention_days))
  const enabled = site.recording_retention_days > 0
  return (
    <div className='space-y-3'>
      <p className='text-sm text-muted-foreground'>
        {enabled
          ? `超过 ${site.recording_retention_days} 天的录像会自动删除本地副本；已保护的录像始终跳过。`
          : '当前不自动删除任何录像。设置天数后，超过该期限的录像会自动删除本地副本（已保护的除外）。'}
      </p>
      <p className='text-xs text-muted-foreground'>
        作用范围：本站点全部通道，与通道绑定哪个存储池无关。按存储池单独设置保留期尚未提供。
      </p>
      <div className='flex flex-wrap items-end gap-2'>
        <SelectField
          ariaLabel='保留天数'
          value={days}
          onValueChange={setDays}
          options={[
            { value: '0', label: '不自动删除' },
            { value: '1', label: '1 天' },
            { value: '3', label: '3 天' },
            { value: '7', label: '7 天' },
            { value: '14', label: '14 天' },
            { value: '30', label: '30 天' },
            { value: '90', label: '90 天' },
            { value: '180', label: '180 天' },
            { value: '365', label: '365 天' },
          ]}
        />
        <Button
          type='button'
          variant='outline'
          disabled={action.pending}
          onClick={() =>
            void action.run(async () => {
              await apiRequest(
                '/api/v1/site',
                jsonRequest(
                  'PATCH',
                  { recording_retention_days: Number(days) },
                  site.version
                )
              )
              onSaved()
            }, Number(days) > 0 ? '已启用本地保留策略' : '已关闭自动删除')
          }
        >
          保存保留策略
        </Button>
      </div>
      <Notices {...action} />
    </div>
  )
}
