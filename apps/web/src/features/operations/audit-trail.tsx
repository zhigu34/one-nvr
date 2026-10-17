import { Fragment, useMemo, useState } from 'react'
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
import { InfoHint } from './info-hint'

/** How many audit rows one page requests. Higher than the shared default because
 *  a single cleanup round collapses dozens of rows into one line, so a page of 50
 *  can render as three visible rows. */
const AUDIT_PAGE_SIZE = 200

/**
 * The audit trail.
 *
 * What it answers: who changed what, and when. Two defects drove this shape:
 *
 *  - The first version printed raw UUIDs for the operator and the object, so the
 *    one question the table exists to answer could not be read off it.
 *  - The second collapsed batches but offered no way back in. A page of fifty
 *    rows could render as three visible lines (one batch of 48 plus two logins),
 *    which reads as missing data. Batches are still collapsed — that is what
 *    makes the list readable — but every collapsed row expands to its members,
 *    and the header states both numbers.
 *
 * The family filter narrows the view; it never deletes evidence. The trail is
 * dominated by playback audits and bulk deletions, so without it a configuration
 * change from this morning sits thousands of rows deep.
 */
export function AuditTrail({ family }: { family: string }) {
  const [cursor, setCursor] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const query = `/api/v1/audit-logs?limit=${AUDIT_PAGE_SIZE}${
    cursor ? '&cursor=' + cursor : ''
  }${family ? '&action_prefix=' + family : ''}`
  const audits = useAPI<PageData<Schema<'Audit'>>>(query, true)
  const items = useMemo(() => audits.data?.items ?? [], [audits.data])
  const groups = useMemo(() => groupAudits(items), [items])

  return (
    <div className='space-y-3'>
      <QueryState
        pending={audits.isPending}
        error={audits.error}
        retry={() => audits.refetch()}
      />
      {items.length === 0 && !audits.isPending && !audits.error && (
        <Empty>该类别下没有操作记录。</Empty>
      )}
      {items.length > 0 && (
        <>
          <p className='text-xs text-muted-foreground'>
            共 {items.length} 条记录
            {groups.length < items.length && `（合并为 ${groups.length} 行）`}
          </p>
          <div className='overflow-x-auto rounded-lg border'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className='w-40'>时间</TableHead>
                  <TableHead>操作</TableHead>
                  <TableHead className='w-40'>操作者</TableHead>
                  <TableHead>对象</TableHead>
                  <TableHead className='w-24' />
                </TableRow>
              </TableHeader>
              <TableBody>
                {groups.map((group) => {
                  const item = group.first
                  const isExpanded = expanded.has(group.key)
                  // A batch of automatic deletions is routine bookkeeping, not an
                  // incident: only a person's destructive action earns the
                  // warning colour. Otherwise the one line an operator must not
                  // miss looks exactly like the thousands that are fine.
                  const alarming =
                    destructiveAction(item.action) && !group.systemActor
                  return (
                    <Fragment key={group.key}>
                      <TableRow>
                        <TableCell className='whitespace-nowrap text-xs text-muted-foreground'>
                          {new Date(item.created_at).toLocaleString('zh-CN', {
                            hour12: false,
                          })}
                        </TableCell>
                        <TableCell>
                          <div className='flex flex-wrap items-baseline gap-x-2'>
                            <span
                              className={alarming ? 'text-amber-500' : undefined}
                            >
                              {actionLabel(item.action)}
                            </span>
                            {actionLabel(item.action) !== item.action && (
                              <span className='font-mono text-[10px] text-muted-foreground/50'>
                                {item.action}
                              </span>
                            )}
                          </div>
                        </TableCell>
                        <TableCell className='whitespace-nowrap text-sm'>
                          {group.systemActor ? (
                            <span className='rounded-full border border-border px-2 py-0.5 text-xs text-muted-foreground'>
                              系统
                            </span>
                          ) : (
                            actorLabel(item.actor_name, item.actor_id)
                          )}
                        </TableCell>
                        <TableCell className='text-sm text-muted-foreground'>
                          {objectLabel(
                            item.object_label,
                            item.object_type,
                            item.action
                          )}
                          {group.count > 1 && (
                            <span className='ml-2 rounded-full border border-border bg-muted px-1.5 py-0.5 text-[11px] text-foreground'>
                              × {group.count}
                            </span>
                          )}
                        </TableCell>
                        <TableCell>
                          {group.count > 1 && (
                            <Button
                              variant='ghost'
                              size='sm'
                              aria-expanded={isExpanded}
                              onClick={() =>
                                setExpanded((current) => {
                                  const next = new Set(current)
                                  if (next.has(group.key)) next.delete(group.key)
                                  else next.add(group.key)
                                  return next
                                })
                              }
                            >
                              {isExpanded ? '收起' : '展开明细'}
                            </Button>
                          )}
                        </TableCell>
                      </TableRow>
                      {isExpanded &&
                        group.items.map((member) => (
                          <TableRow
                            key={member.id}
                            className='bg-muted/30 text-xs'
                          >
                            <TableCell className='whitespace-nowrap pl-8 text-muted-foreground'>
                              {new Date(member.created_at).toLocaleString(
                                'zh-CN',
                                { hour12: false }
                              )}
                            </TableCell>
                            <TableCell className='text-muted-foreground'>
                              {actionLabel(member.action)}
                            </TableCell>
                            <TableCell className='text-muted-foreground'>
                              {group.systemActor
                                ? '系统'
                                : actorLabel(member.actor_name, member.actor_id)}
                            </TableCell>
                            <TableCell className='text-muted-foreground'>
                              {objectLabel(
                                member.object_label,
                                member.object_type,
                                member.action
                              )}
                            </TableCell>
                            <TableCell />
                          </TableRow>
                        ))}
                    </Fragment>
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

/** The family selector, rendered by the card header so it sits on the title line
 *  like every other filter on this page. */
export function AuditFamilyFilter({
  value,
  onChange,
}: {
  value: string
  onChange: (value: string) => void
}) {
  return (
    <div className='flex items-center gap-2'>
      <SelectField
        ariaLabel='操作类别'
        name='audit_family'
        value={value}
        onValueChange={onChange}
        className='w-40'
        options={AUDIT_FAMILIES.map((item) => ({
          value: item.value,
          label: item.label,
        }))}
      />
      <InfoHint label='审计筛选说明'>
        按类别筛选不删除任何记录：播放与批量清理条目会淹没配置变更，缩窄视图比删证据更合适。
        同一秒内、同一操作、同一操作者的批量记录会合并为一行，可展开查看原始记录。
      </InfoHint>
    </div>
  )
}
