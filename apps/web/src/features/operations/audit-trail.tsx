import { useState } from 'react'
import type { Schema, PageData } from '@/lib/types'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useAPI } from '@/features/foundation/hooks'
import { QueryState, Empty, SelectField } from '@/features/foundation/ui'
import {
  AUDIT_FAMILIES,
  actionLabel,
  actorLabel,
  destructiveAction,
  groupAudits,
  objectLabel,
} from './audit-format'

/**
 * The audit trail.
 *
 * What it answers: who changed what, and when. What it deliberately does not do:
 * show raw UUIDs or raw action codes. Both were present in the first version and
 * made the table unreadable — every row said `7b6fdf36-...` for the operator and
 * a second UUID for the object, so the one question the page exists to answer
 * ("who deleted these recordings") could not be answered from it.
 *
 * The family filter exists because the trail is dominated by two kinds of
 * entries: routine playback audits and bulk retention deletions. Without a
 * filter, a configuration change from this morning is 2000 rows deep. Deleting
 * the volume entries would destroy audit evidence, so the page narrows the view
 * instead.
 */
export function AuditTrail({
  displayTime,
}: {
  displayTime: (value: string) => string
}) {
  const [cursor, setCursor] = useState('')
  const [family, setFamily] = useState('')
  const query = `/api/v1/audit-logs?limit=50${cursor ? '&cursor=' + cursor : ''}${
    family ? '&action_prefix=' + family : ''
  }`
  const audits = useAPI<PageData<Schema<'Audit'>>>(query, true)
  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-end gap-3'>
        <SelectField
          label='操作类别'
          name='audit_family'
          value={family}
          onValueChange={(value) => {
            setFamily(value)
            // A cursor from the previous filter would point into a different
            // result set; restarting at the newest row is the only coherent
            // continuation.
            setCursor('')
          }}
          options={AUDIT_FAMILIES.map((item) => ({
            value: item.value,
            label: item.label,
          }))}
        />
        <p className='pb-2 text-xs text-muted-foreground'>
          按类别筛选不删除任何记录：播放与批量清理条目会淹没配置变更，缩窄视图比删证据更合适。
        </p>
      </div>
      <QueryState
        pending={audits.isPending}
        error={audits.error}
        retry={() => audits.refetch()}
      />
      {audits.data?.items.length === 0 && <Empty>该类别下没有操作记录。</Empty>}
      {(audits.data?.items.length ?? 0) > 0 && (
        <>
          <div className='overflow-x-auto rounded-lg border'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className='w-40'>时间</TableHead>
                  <TableHead>操作</TableHead>
                  <TableHead>操作者</TableHead>
                  <TableHead>对象</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {groupAudits(audits.data?.items ?? []).map((group) => {
                  const item = group.first
                  return (
                  <TableRow key={group.key}>
                    <TableCell className='whitespace-nowrap text-xs text-muted-foreground'>
                      {displayTime(item.created_at)}
                    </TableCell>
                    <TableCell>
                      <div className='flex flex-wrap items-baseline gap-x-2'>
                        <span
                          className={
                            destructiveAction(item.action)
                              ? 'text-amber-500'
                              : undefined
                          }
                        >
                          {actionLabel(item.action)}
                        </span>
                        {/* The raw code stays for support, but at a size that
                            does not compete with the label. A translated action
                            is the primary information; the code is the lookup
                            key for a log search. */}
                        {actionLabel(item.action) !== item.action && (
                          <span className='font-mono text-[10px] text-muted-foreground/50'>
                            {item.action}
                          </span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className='whitespace-nowrap text-sm'>
                      {actorLabel(item.actor_name, item.actor_id)}
                    </TableCell>
                    <TableCell className='text-sm text-muted-foreground'>
                      {objectLabel(item.object_label, item.object_type)}
                      {group.count > 1 && (
                        /* The count is the honest presentation of a batch: the
                           sweep really did remove this many, and collapsing
                           them into one line must not make it look like one. */
                        <span className='ml-2 rounded-full border border-border bg-muted px-1.5 py-0.5 text-[11px] text-foreground'>
                          × {group.count}
                        </span>
                      )}
                    </TableCell>
                  </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>
          <div className='flex gap-3'>
            {audits.data?.next_cursor && (
              <Button
                variant='outline'
                onClick={() => setCursor(audits.data!.next_cursor!)}
              >
                更早记录
              </Button>
            )}
            {cursor && (
              <Button variant='ghost' onClick={() => setCursor('')}>
                返回最新
              </Button>
            )}
          </div>
        </>
      )}
    </div>
  )
}
