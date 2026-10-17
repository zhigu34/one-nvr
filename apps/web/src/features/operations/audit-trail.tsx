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
import { useQueries } from '@tanstack/react-query'
import { apiRequest } from '@/lib/api-client'
import { QueryState, Empty, SelectField } from '@/features/foundation/ui'
import {
  AUDIT_FAMILIES,
  actionLabel,
  actorLabel,
  destructiveAction,
  groupAudits,
  isSystemCleanup,
  objectLabel,
} from './audit-format'
import { Switch } from '@/components/ui/switch'
import { InfoHint } from './info-hint'
import { Pager } from './pager'
import { slicePage } from './operations-format'

/** How many audit rows one page requests.
 *
 *  Much higher than the shared default because a single cleanup round collapses
 *  hundreds of rows into one line: this site's most recent 1200 rows were one
 *  deletion batch, so a smaller page renders as three visible rows and buries the
 *  operator's own actions. */
const AUDIT_PAGE_SIZE = 500

/** Below this many visible rows the trail keeps pulling older pages: a page can
 *  collapse into a handful of lines and an empty-looking card reads as a bug. */
const MIN_VISIBLE_ROWS = 10

/**
 * Hard stop for the automatic fill. This site's log holds 2028 rows of which 1246
 * are cleanup, so reaching the operator's own actions needs more than a few
 * pages; twenty pages is 4000 rows, enough for a realistic log while still
 * bounding a pathological one.
 */
const MAX_AUTO_PAGES = 20

/** How many members one expanded batch shows before stopping. The rest stay
 *  reachable through the count and the family filter; rendering 107 rows inline
 *  buries the rest of the page. */
const EXPAND_LIMIT = 25

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
export function AuditTrail({
  family,
  hideCleanup = false,
}: {
  family: string
  hideCleanup?: boolean
}) {
  const [cursor, setCursor] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [page, setPage] = useState(1)
  // A single cleanup round can fill an entire page with rows that collapse into
  // one line, so a correct page of 200 can render as four visible rows — which
  // reads as missing data. While the visible list is thin and the server still
  // has history, the next cursor joins the set of pages to load. This is derived
  // during render rather than appended from an effect: the effect version wrote
  // state while rendering the same data it read, and React flagged the loop.
  const [autoCursors, setAutoCursors] = useState<string[]>([])
  const pages = useMemo(
    () => [cursor, ...autoCursors],
    [cursor, autoCursors]
  )
  const paths = useMemo(
    () =>
      pages.map(
        (page) =>
          `/api/v1/audit-logs?limit=${AUDIT_PAGE_SIZE}${
            page ? '&cursor=' + page : ''
          }${family ? '&action_prefix=' + family : ''}`
      ),
    [pages, family]
  )
  const responses = useQueries({
    queries: paths.map((path) => ({
      queryKey: [path],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        apiRequest<PageData<Schema<'Audit'>>>(path, { signal }),
      retry: false,
    })),
  })

  // Plain derivation, no memo: this is at most a few hundred rows and the
  // grouping is a single pass. Memoising it bought nothing and cost a
  // dependency-array argument with a hook that returns fresh identities.
  const loaded = responses
    .map((response) => response.data)
    .filter((page): page is PageData<Schema<'Audit'>> => !!page)
  const all = loaded.flatMap((entry) => entry.items)
  // Hiding routine cleanup is a view filter, not a deletion: the records stay in
  // the log and the header still reports the full total.
  const items = hideCleanup ? all.filter((item) => !isSystemCleanup(item)) : all
  const groups = groupAudits(items)
  const paged = slicePage(groups, page)
  const visible = paged.rows

  // A single cleanup round can fill an entire page with rows that collapse into
  // one line, so a correct page of 200 can render as four visible rows. That
  // reads as missing data.
  //
  // The test is on *records*, not on rendered groups. One cleanup group can
  // stand for 1198 rows: counting it as "one visible row" made a page of nothing
  // but cleanup look like a thin result, and the auto-fill stopped after five
  // pages — 1200 rows — with the operator's own actions still below the fold.
  // What matters is whether the non-cleanup records have been reached.
  const last = loaded[loaded.length - 1]
  const nextCursor = last?.next_cursor ?? null
  const pendingPage = responses[responses.length - 1]
  const substantive = items.filter((item) => !isSystemCleanup(item)).length
  const thin = groups.length < MIN_VISIBLE_ROWS || substantive < MIN_VISIBLE_ROWS
  const canExtend = thin && !!nextCursor && autoCursors.length < MAX_AUTO_PAGES
  // Adopt the next cursor once the page that produced it has settled. Guarded by
  // "not already queued" so a settled render cannot queue the same page twice.
  if (
    canExtend &&
    !pendingPage?.isPending &&
    nextCursor &&
    !autoCursors.includes(nextCursor) &&
    !pages.includes(nextCursor)
  ) {
    setAutoCursors([...autoCursors, nextCursor])
  }

  return (
    <div className='space-y-3'>
      <QueryState
        pending={responses[0]?.isPending}
        error={responses[0]?.error}
        retry={() => void responses[0]?.refetch()}
      />
      {items.length === 0 && !responses[0]?.isPending && !responses[0]?.error && (
        <Empty>该类别下没有操作记录。</Empty>
      )}
      {items.length > 0 && (
        <>
          <p className='text-xs text-muted-foreground'>
            共 {items.length} 条记录
            {groups.length < items.length && `（合并为 ${groups.length} 行）`}
            {all.length > items.length && ` · 共载入 ${all.length} 条`}
            {hideCleanup && all.length !== items.length && (
              <span> · 已隐藏 {all.length - items.length} 条系统清理</span>
            )}
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
                {visible.map((group) => {
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
                          {group.systemActor &&
                          isSystemCleanup(item) &&
                          group.count > 1
                            ? '批量清理历史录像'
                            : objectLabel(
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
                      {/* A batch can hold hundreds of rows. Expanded inline they
                          would push everything below off the page, so the detail
                          is capped and scrolls — the same treatment the fault
                          list uses for a flapping stream. */}
                      {isExpanded &&
                        group.items.slice(0, EXPAND_LIMIT).map((member) => (
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
                      {isExpanded && group.count > EXPAND_LIMIT && (
                        <TableRow className='bg-muted/30'>
                          <TableCell
                            colSpan={5}
                            className='text-xs text-muted-foreground'
                          >
                            仅显示前 {EXPAND_LIMIT} 条，其余 {group.count - EXPAND_LIMIT}{' '}
                            条按同一批次记录在审计库中
                          </TableCell>
                        </TableRow>
                      )}
                    </Fragment>
                  )
                })}
              </TableBody>
            </Table>
          </div>
          <Pager
            page={paged.page}
            pageCount={paged.pageCount}
            rowCount={groups.length}
            onPage={setPage}
            note={
              groups.length > 0 && groups.length < items.length
                ? `${items.length} 条记录合并为 ${groups.length} 行`
                : undefined
            }
          />
          <div className='flex items-center gap-3'>
            {nextCursor && (
              <Button
                variant='outline'
                onClick={() => {
                  setCursor(nextCursor)
                  setAutoCursors([])
                  setExpanded(new Set())
                }}
              >
                更早记录
              </Button>
            )}
            {(cursor || autoCursors.length > 0) && (
              <Button
                variant='ghost'
                onClick={() => {
                  setCursor('')
                  setAutoCursors([])
                  setExpanded(new Set())
                }}
              >
                返回最新
              </Button>
            )}
            {pendingPage?.isPending && (
              <span className='text-xs text-muted-foreground'>加载更多…</span>
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
  hideCleanup,
  onHideCleanupChange,
}: {
  value: string
  onChange: (value: string) => void
  hideCleanup: boolean
  onHideCleanupChange: (value: boolean) => void
}) {
  return (
    <div className='flex items-center gap-3'>
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
      <label className='flex items-center gap-2 text-xs text-muted-foreground'>
        <Switch
          checked={hideCleanup}
          onCheckedChange={onHideCleanupChange}
          aria-label='隐藏系统清理'
        />
        隐藏系统清理
      </label>
      <InfoHint label='审计筛选说明'>
        按类别筛选不删除任何记录：播放与批量清理条目会淹没配置变更，缩窄视图比删证据更合适。
        同一秒内、同一操作、同一操作者的批量记录会合并为一行，可展开查看原始记录。
      </InfoHint>
    </div>
  )
}
